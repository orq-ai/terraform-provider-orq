// Package provider implements the orq Terraform provider on
// terraform-plugin-framework.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

const (
	defaultURL = "https://my.orq.ai"
	envURL     = "ORQ_API_BASE_URL"
	envAPIKey  = "ORQ_API_KEY"
)

// Ensure orqProvider satisfies the framework interface.
var _ provider.Provider = &orqProvider{}

type orqProvider struct {
	version string
}

// New returns the provider factory used by main and by acceptance tests.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &orqProvider{version: version}
	}
}

// providerModel maps the provider "orq" HCL block.
type providerModel struct {
	URL    types.String `tfsdk:"url"`
	APIKey types.String `tfsdk:"api_key"`
}

func (p *orqProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "orq"
	resp.Version = p.version
}

func (p *orqProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Provider for orq.ai workspace resources. Authenticates with an opaque " +
			"management key (`sk-orq-...`); the workspace is implied by the credential.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Base URL of the orq API. Falls back to the `ORQ_API_BASE_URL` environment " +
					"variable, then `https://my.orq.ai`. Override for on-prem installs or staging.",
			},
			"api_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Management key (`sk-orq-...`). Falls back to the `ORQ_API_KEY` " +
					"environment variable (like `GOOGLE_CREDENTIALS`). An explicit value beats the env var.",
			},
		},
	}
}

// resolveString implements the precedence: explicit attribute > env var > fallback.
func resolveString(attr types.String, envKey, fallback string) string {
	if !attr.IsNull() && !attr.IsUnknown() {
		return attr.ValueString()
	}
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return fallback
}

func (p *orqProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown values (interpolated from resources) can't be resolved at configure
	// time — fail clearly rather than silently falling back to an env var.
	if cfg.URL.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Unknown provider URL",
			"The url value is unknown at configuration time. Use a static value, ORQ_API_BASE_URL, or the default.")
	}
	if cfg.APIKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Unknown provider api_key",
			"The api_key value is unknown at configuration time. Use a static value or the ORQ_API_KEY env var.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	url := resolveString(cfg.URL, envURL, defaultURL)
	apiKey := resolveString(cfg.APIKey, envAPIKey, "")

	// Structural validation ONLY. Credential validation is LAZY: the first real
	// API call (e.g. the orq_projects data source's ListProjects) surfaces auth
	// errors naming ORQ_API_KEY. We deliberately do NOT probe
	// /v2/management-keys/capabilities — it is a PUBLIC route and cannot
	// validate a credential, and a narrowly-scoped but valid key must not
	// hard-fail provider init.
	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing orq management key",
			"Set the `api_key` attribute or the ORQ_API_KEY environment variable to an sk-orq-... management key.")
	}
	// Full structural URL validation: an absolute http(s) URL with a host, no
	// embedded userinfo/query/fragment. A bare `https://` or a hostless value is
	// rejected here rather than surfacing as an opaque dial error at first use.
	if url == "" {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing orq URL",
			"Set the `url` attribute or the ORQ_API_BASE_URL environment variable.")
	} else if _, err := client.ParseBaseURL(url); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Malformed orq URL", err.Error()+" (got: "+url+")")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := client.New(client.Config{URL: url, Token: apiKey})
	if err != nil {
		resp.Diagnostics.AddError("Failed to build orq client", err.Error())
		return
	}

	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *orqProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewProjectsDataSource,
	}
}

func (p *orqProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewProjectResource,
		NewBudgetResource,
		NewNotifierResource,
		NewGuardrailRuleResource,
		NewWorkspaceModelResource,
		NewRoutingRuleResource,
		NewAPIKeyResource,
		NewManagementKeyResource,
		NewModelResource,
		NewBedrockModelResource,
		NewWorkspaceSettingsResource,
		NewEvaluatorResource,
	}
}
