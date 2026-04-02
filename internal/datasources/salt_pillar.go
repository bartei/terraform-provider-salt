package datasources

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/stefanob/terraform-provider-salt/pkg/ssh"
)

var _ datasource.DataSource = &SaltPillarDataSource{}

type SaltPillarDataSource struct{}

type SaltPillarModel struct {
	ID         types.String `tfsdk:"id"`
	Host       types.String `tfsdk:"host"`
	Port       types.Int64  `tfsdk:"port"`
	User       types.String `tfsdk:"user"`
	PrivateKey types.String `tfsdk:"private_key"`
	Values     types.Map    `tfsdk:"values"`
}

func NewSaltPillarDataSource() func() datasource.DataSource {
	return func() datasource.DataSource {
		return &SaltPillarDataSource{}
	}
}

func (d *SaltPillarDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pillar"
}

func (d *SaltPillarDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads Salt pillar data from a remote host. Returns the rendered pillar as a flat string map.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Data source identifier.",
				Computed:    true,
			},
			"host": schema.StringAttribute{
				Description: "Target host address.",
				Required:    true,
			},
			"port": schema.Int64Attribute{
				Description: "SSH port. Defaults to 22.",
				Optional:    true,
			},
			"user": schema.StringAttribute{
				Description: "SSH user.",
				Required:    true,
			},
			"private_key": schema.StringAttribute{
				Description: "SSH private key contents.",
				Required:    true,
				Sensitive:   true,
			},
			"values": schema.MapAttribute{
				Description: "Map of pillar keys to their string values. Nested values are JSON-encoded.",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (d *SaltPillarDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SaltPillarModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	port := int64(22)
	if !data.Port.IsNull() && !data.Port.IsUnknown() {
		port = data.Port.ValueInt64()
	}

	client, err := ssh.NewClientWithRetry(ssh.ConnectConfig{
		Host:          data.Host.ValueString(),
		Port:          int(port),
		User:          data.User.ValueString(),
		PrivateKey:    data.PrivateKey.ValueString(),
		MaxRetries:    3,
		RetryInterval: 5 * time.Second,
	})
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("SSH connection to %s failed", data.Host.ValueString()),
			err.Error(),
		)
		return
	}
	defer func() { _ = client.Close() }()

	r, err := client.RunCapture("sudo salt-call --local pillar.items --out=json --out-file=/dev/stdout")
	if err != nil {
		resp.Diagnostics.AddError("Failed to read pillar", err.Error())
		return
	}
	if r.ExitCode != 0 {
		resp.Diagnostics.AddError(
			"salt-call pillar.items failed",
			fmt.Sprintf("Exit code %d.\n\nstderr:\n%s", r.ExitCode, r.Stderr),
		)
		return
	}

	// Parse {"local": {"key": "value", ...}}
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.Stdout), &raw); err != nil {
		resp.Diagnostics.AddError("Failed to parse pillar output", err.Error())
		return
	}

	local, ok := raw["local"]
	if !ok {
		resp.Diagnostics.AddError("Unexpected pillar output", "Missing 'local' key in salt-call output")
		return
	}

	// Flatten all pillar values to strings
	flatPillar := make(map[string]string, len(local))
	for k, v := range local {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			flatPillar[k] = s
			continue
		}
		flatPillar[k] = string(v)
	}

	pillarMap, diags := types.MapValueFrom(ctx, types.StringType, flatPillar)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(data.Host.ValueString())
	data.Values = pillarMap
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
