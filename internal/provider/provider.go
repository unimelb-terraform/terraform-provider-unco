package provider

import (
	"context"
	"os"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"sigs.k8s.io/yaml"
)

var _ provider.Provider = &UncoProvider{}

type UncoProvider struct {
	version string
}

type UncoProviderModel struct {
	APIURL   types.String `tfsdk:"api_url"`
	ClientID types.String `tfsdk:"client_id"`
	TenantID types.String `tfsdk:"tenant_id"`
	Token    types.String `tfsdk:"token"`
}

type localCLIConfig struct {
	API struct {
		URL      string `json:"url"`
		TenantID string `json:"tenantId"`
		ClientID string `json:"clientId"`
	} `json:"api"`
}

func loadCLIConfigDefaults() (apiURL, clientID, tenantID string) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", "", ""
	}
	raw, err := os.ReadFile(filepath.Join(cfgDir, "unco", "config.yaml"))
	if err != nil {
		return "", "", ""
	}
	var parsed localCLIConfig
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return "", "", ""
	}
	return parsed.API.URL, parsed.API.ClientID, parsed.API.TenantID
}

func (p *UncoProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "unco"
	resp.Version = p.version
}

func (p *UncoProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Terraform provider for Unco v2 variable sets and stack actions.",
		Attributes: map[string]schema.Attribute{
			"api_url": schema.StringAttribute{
				MarkdownDescription: "Unco API base URL (overrides UNCO_API_URL and ~/.config/unco/config.yaml).",
				Optional:            true,
			},
			"client_id": schema.StringAttribute{
				MarkdownDescription: "Entra ID client ID for Unco API authentication (overrides UNCO_CLIENT_ID and ~/.config/unco/config.yaml).",
				Optional:            true,
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Entra ID tenant ID for Unco API authentication (overrides UNCO_TENANT_ID and ~/.config/unco/config.yaml).",
				Optional:            true,
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "Optional bearer token for Unco API requests (overrides UNCO_API_TOKEN and MSAL token acquisition).",
				Optional:            true,
				Sensitive:           true,
			},
		},
	}
}

func (p *UncoProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data UncoProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if data.APIURL.IsUnknown() || data.ClientID.IsUnknown() || data.TenantID.IsUnknown() || data.Token.IsUnknown() {
		return
	}

	apiURL, clientID, tenantID := loadCLIConfigDefaults()

	if v := os.Getenv(EnvUncoAPIURL); v != "" {
		apiURL = v
	}
	if v := os.Getenv(EnvUncoClientID); v != "" {
		clientID = v
	}
	if v := os.Getenv(EnvUncoTenantID); v != "" {
		tenantID = v
	}

	if !data.APIURL.IsNull() && !data.APIURL.IsUnknown() && data.APIURL.ValueString() != "" {
		apiURL = data.APIURL.ValueString()
	}
	if !data.ClientID.IsNull() && !data.ClientID.IsUnknown() && data.ClientID.ValueString() != "" {
		clientID = data.ClientID.ValueString()
	}
	if !data.TenantID.IsNull() && !data.TenantID.IsUnknown() && data.TenantID.ValueString() != "" {
		tenantID = data.TenantID.ValueString()
	}

	if apiURL == "" {
		resp.Diagnostics.AddError(
			"Missing Unco API URL",
			"api_url must be configured in the provider block, via UNCO_API_URL, or in ~/.config/unco/config.yaml",
		)
		return
	}

	token := os.Getenv(EnvUncoAPIToken)
	if !data.Token.IsNull() && !data.Token.IsUnknown() && data.Token.ValueString() != "" {
		token = data.Token.ValueString()
	}

	if token == "" {
		var err error
		token, err = acquireUncoToken(ctx, clientID, tenantID)
		if err != nil {
			resp.Diagnostics.AddError("Unco Authentication Error", err.Error())
			return
		}
	}

	ua := "terraform-provider-unco/dev"
	if p.version != "" {
		ua = "terraform-provider-unco/" + p.version
	}
	pc := NewProviderClient(apiURL, token, ua)
	resp.ResourceData = pc
	resp.DataSourceData = pc
}

func (p *UncoProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewVariableEntryResource,
		NewVariableEntryPatchResource,
		NewVariableEntryDeleteResource,
		NewPlanActionResource,
	}
}

func (p *UncoProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &UncoProvider{version: version}
	}
}
