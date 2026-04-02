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

var _ datasource.DataSource = &SaltGrainsDataSource{}

type SaltGrainsDataSource struct{}

type SaltGrainsModel struct {
	ID         types.String `tfsdk:"id"`
	Host       types.String `tfsdk:"host"`
	Port       types.Int64  `tfsdk:"port"`
	User       types.String `tfsdk:"user"`
	PrivateKey types.String `tfsdk:"private_key"`
	Values     types.Map    `tfsdk:"values"`
}

func NewSaltGrainsDataSource() func() datasource.DataSource {
	return func() datasource.DataSource {
		return &SaltGrainsDataSource{}
	}
}

func (d *SaltGrainsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_grains"
}

func (d *SaltGrainsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads Salt grains from a remote host. Grains are system properties like OS, CPU, memory, etc.",
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
				Description: "Map of grain names to their string values.",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (d *SaltGrainsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SaltGrainsModel
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

	r, err := client.RunCapture("sudo salt-call --local grains.items --out=json --out-file=/dev/stdout")
	if err != nil {
		resp.Diagnostics.AddError("Failed to read grains", err.Error())
		return
	}
	if r.ExitCode != 0 {
		resp.Diagnostics.AddError(
			"salt-call grains.items failed",
			fmt.Sprintf("Exit code %d.\n\nstderr:\n%s", r.ExitCode, r.Stderr),
		)
		return
	}

	// Parse {"local": {"os": "Debian", ...}}
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.Stdout), &raw); err != nil {
		resp.Diagnostics.AddError("Failed to parse grains output", err.Error())
		return
	}

	local, ok := raw["local"]
	if !ok {
		resp.Diagnostics.AddError("Unexpected grains output", "Missing 'local' key in salt-call output")
		return
	}

	// Flatten all grain values to strings
	flatGrains := make(map[string]string, len(local))
	for k, v := range local {
		// Try to unmarshal as string first
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			flatGrains[k] = s
			continue
		}
		// Fall back to the raw JSON representation
		flatGrains[k] = string(v)
	}

	grainsMap, diags := types.MapValueFrom(ctx, types.StringType, flatGrains)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(data.Host.ValueString())
	data.Values = grainsMap
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
