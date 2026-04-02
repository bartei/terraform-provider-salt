package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/stefanob/terraform-provider-salt/internal/datasources"
	"github.com/stefanob/terraform-provider-salt/internal/resources"
)

var _ provider.Provider = &SaltProvider{}

type SaltProvider struct {
	version string
}

type SaltProviderModel struct {
	SaltVersion types.String `tfsdk:"salt_version"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &SaltProvider{
			version: version,
		}
	}
}

func (p *SaltProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "salt"
	resp.Version = p.version
}

func (p *SaltProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Salt provider applies Salt states to remote hosts in masterless mode via SSH.",
		Attributes: map[string]schema.Attribute{
			"salt_version": schema.StringAttribute{
				Description: "Default Salt version to install on targets. Can be overridden per resource.",
				Optional:    true,
			},
		},
	}
}

func (p *SaltProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config SaltProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	saltVersion := ""
	if !config.SaltVersion.IsNull() {
		saltVersion = config.SaltVersion.ValueString()
	}

	resp.ResourceData = saltVersion
}

func (p *SaltProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		resources.NewSaltStateResource(),
		resources.NewSaltFormulaResource(),
	}
}

func (p *SaltProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		datasources.NewSaltGrainsDataSource(),
		datasources.NewSaltPillarDataSource(),
	}
}
