package provider

import (
	"context"
	"fmt"
	"reflect"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                = &mcpServerResource{}
	_ resource.ResourceWithConfigure   = &mcpServerResource{}
	_ resource.ResourceWithImportState = &mcpServerResource{}
)

var mcpKeyValidators = []validator.String{
	stringvalidator.LengthBetween(1, 64),
	stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9_-]+$`), "must contain only lowercase letters, digits, hyphens and underscores"),
}

func NewMcpServerResource() resource.Resource { return &mcpServerResource{} }

type mcpServerResource struct {
	servers client.McpServersAPI
}

type mcpServerResourceModel struct {
	ID                  types.String          `tfsdk:"id"`
	Key                 types.String          `tfsdk:"key"`
	DisplayName         types.String          `tfsdk:"display_name"`
	Description         types.String          `tfsdk:"description"`
	Upstream            *mcpUpstreamModel     `tfsdk:"upstream"`
	Auth                *mcpAuthModel         `tfsdk:"auth"`
	DefaultToolExposure *mcpToolExposureModel `tfsdk:"default_tool_exposure"`
	OpenAPI             *mcpOpenAPIModel      `tfsdk:"openapi"`
	Sharing             *mcpSharingModel      `tfsdk:"sharing"`
	CreatedAt           types.String          `tfsdk:"created_at"`
	UpdatedAt           types.String          `tfsdk:"updated_at"`
}

type mcpUpstreamModel struct {
	Type types.String `tfsdk:"type"`
	URL  types.String `tfsdk:"url"`
}

type mcpAuthModel struct {
	Type          types.String   `tfsdk:"type"`
	StaticHeaders types.Map      `tfsdk:"static_headers"`
	OAuth         *mcpOAuthModel `tfsdk:"oauth"`
}

type mcpOAuthModel struct {
	ClientID     types.String `tfsdk:"client_id"`
	ClientSecret types.String `tfsdk:"client_secret"`
	TokenURL     types.String `tfsdk:"token_url"`
	Scopes       types.List   `tfsdk:"scopes"`
	Issuer       types.String `tfsdk:"issuer"`
	Resource     types.String `tfsdk:"resource"`
}

type mcpOpenAPIModel struct {
	DocumentURL types.String `tfsdk:"document_url"`
	Document    types.String `tfsdk:"document"`
	Operations  types.List   `tfsdk:"operations"`
}

func (r *mcpServerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_server"
}

func (r *mcpServerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An upstream MCP server registered in the workspace. Creating a server does not sync " +
			"its tools: a gateway can only link an `HTTP` or `SSE` server after its tools were synced once " +
			"(in the orq UI or through the sync API). `OPENAPI` servers compile their tools on create. " +
			"Changing `upstream` or `auth` makes orq dial the upstream and rejects the update when it is unreachable.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "MCP server ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"key": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Lowercase slug (letters, digits, hyphens, underscores; max 64), unique per " +
					"workspace. Prefixes this server's tool names in gateways. Immutable: changing it forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    mcpKeyValidators,
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable name.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form note about what this server is for.",
			},
			"upstream": schema.SingleNestedAttribute{
				Required:            true,
				MarkdownDescription: "How orq dials the upstream server.",
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						Required: true,
						MarkdownDescription: "Transport: `HTTP`, `SSE` or `OPENAPI` (tools are the operations of an " +
							"OpenAPI document and `url` is the API base URL). Switching to or from `OPENAPI` forces replacement.",
						Validators: []validator.String{stringvalidator.OneOf(
							client.McpConnectionTypeHTTP, client.McpConnectionTypeSSE, client.McpConnectionTypeOpenAPI,
						)},
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(
							crossesOpenAPIConnectionType,
							"Switching to or from OPENAPI forces replacement.",
							"Switching to or from `OPENAPI` forces replacement.",
						)},
					},
					"url": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Upstream endpoint (http or https).",
					},
				},
			},
			"auth": schema.SingleNestedAttribute{
				Required:            true,
				MarkdownDescription: "Credentials orq sends upstream. Use `type = \"NONE\"` for public servers.",
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						Required: true,
						MarkdownDescription: "`NONE`, `STATIC_HEADERS`, `OAUTH_CLIENT_CREDENTIALS`, `PER_USER_OAUTH` or " +
							"`ENTERPRISE_MANAGED` (requires Cross App Access on the workspace's SSO configuration).",
						Validators: []validator.String{stringvalidator.OneOf(
							client.McpAuthTypeNone, client.McpAuthTypeStaticHeaders, client.McpAuthTypeOAuthClientCredentials,
							client.McpAuthTypePerUserOAuth, client.McpAuthTypeEnterpriseManaged,
						)},
					},
					"static_headers": schema.MapAttribute{
						Optional:    true,
						Sensitive:   true,
						ElementType: types.StringType,
						MarkdownDescription: "Headers sent upstream on every request, by header name; required when " +
							"`type` is `STATIC_HEADERS`. Write-only: orq never returns the values, so the provider " +
							"keeps the configured ones in state and cannot detect out-of-band changes.",
					},
					"oauth": schema.SingleNestedAttribute{
						Optional:            true,
						MarkdownDescription: "OAuth client presented to the upstream authorization server, for the OAuth auth types.",
						Attributes: map[string]schema.Attribute{
							"client_id": schema.StringAttribute{
								Optional:            true,
								MarkdownDescription: "OAuth client identifier.",
							},
							"client_secret": schema.StringAttribute{
								Optional:  true,
								Sensitive: true,
								MarkdownDescription: "OAuth client secret. Write-only: orq never returns it, so the " +
									"provider keeps the configured value in state.",
							},
							"token_url": schema.StringAttribute{
								Optional:            true,
								MarkdownDescription: "Token endpoint; only for `OAUTH_CLIENT_CREDENTIALS`.",
							},
							"scopes": schema.ListAttribute{
								Optional:            true,
								ElementType:         types.StringType,
								MarkdownDescription: "Scopes requested when minting upstream access tokens.",
							},
							"issuer": schema.StringAttribute{
								Optional:            true,
								MarkdownDescription: "Exact authorization-server issuer; pins discovery for `PER_USER_OAUTH` and `ENTERPRISE_MANAGED`.",
							},
							"resource": schema.StringAttribute{
								Optional:            true,
								MarkdownDescription: "Protected resource identifier. Omitted uses the upstream URL.",
							},
						},
					},
				},
			},
			"default_tool_exposure": schema.SingleNestedAttribute{
				Required:            true,
				MarkdownDescription: "Fallback exposure used by any gateway link that does not set its own.",
				Attributes:          mcpToolExposureAttributes(),
			},
			"openapi": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Source of the tools of an `OPENAPI` server; set `document` or `document_url`. " +
					"Changing it recompiles the tools.",
				Attributes: map[string]schema.Attribute{
					"document_url": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "URL the OpenAPI document is fetched from.",
					},
					"document": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "OpenAPI 3.x document as JSON or YAML. Write-only: orq compiles it and " +
							"never returns it, so the provider keeps the configured value in state.",
					},
					"operations": schema.ListAttribute{
						Optional:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "Tool names of the operations to expose. Omitted exposes every operation.",
					},
				},
			},
			"sharing": mcpSharingAttribute("server"),
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation time (RFC 3339).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last update time (RFC 3339).",
			},
		},
	}
}

func crossesOpenAPIConnectionType(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	wasOpenAPI := req.StateValue.ValueString() == client.McpConnectionTypeOpenAPI
	willBeOpenAPI := req.PlanValue.ValueString() == client.McpConnectionTypeOpenAPI
	resp.RequiresReplace = wasOpenAPI != willBeOpenAPI
}

func (r *mcpServerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.servers = c.McpServers()
}

func (u *mcpUpstreamModel) toClient() client.McpConnection {
	return client.McpConnection{Type: u.Type.ValueString(), URL: u.URL.ValueString()}
}

func (a *mcpAuthModel) toClient(ctx context.Context) (client.McpAuth, diag.Diagnostics) {
	headers, diags := stringMap(ctx, a.StaticHeaders)
	out := client.McpAuth{Type: a.Type.ValueString(), StaticHeaders: headers}
	if a.OAuth == nil {
		return out, diags
	}
	scopes, scopeDiags := stringSlice(ctx, a.OAuth.Scopes)
	diags.Append(scopeDiags...)
	out.OAuth = &client.McpOAuth{
		ClientID:     a.OAuth.ClientID.ValueString(),
		ClientSecret: a.OAuth.ClientSecret.ValueString(),
		TokenURL:     a.OAuth.TokenURL.ValueString(),
		Scopes:       scopes,
		Issuer:       a.OAuth.Issuer.ValueString(),
		Resource:     a.OAuth.Resource.ValueString(),
	}
	return out, diags
}

func (o *mcpOpenAPIModel) toClient(ctx context.Context) (*client.McpOpenAPI, diag.Diagnostics) {
	if o == nil {
		return nil, nil
	}
	operations, diags := stringSlice(ctx, o.Operations)
	return &client.McpOpenAPI{
		Document:    o.Document.ValueString(),
		DocumentURL: o.DocumentURL.ValueString(),
		Operations:  operations,
	}, diags
}

type mcpServerWrite struct {
	connection client.McpConnection
	auth       client.McpAuth
	exposure   client.McpToolExposure
	openAPI    *client.McpOpenAPI
	sharing    *client.McpSharing
}

func (m *mcpServerResourceModel) toWrite(ctx context.Context) (mcpServerWrite, diag.Diagnostics) {
	var diags diag.Diagnostics
	var partial diag.Diagnostics
	write := mcpServerWrite{connection: m.Upstream.toClient()}
	write.auth, partial = m.Auth.toClient(ctx)
	diags.Append(partial...)
	write.exposure, partial = m.DefaultToolExposure.toClient(ctx)
	diags.Append(partial...)
	write.openAPI, partial = m.OpenAPI.toClient(ctx)
	diags.Append(partial...)
	write.sharing, partial = m.Sharing.toClient(ctx)
	diags.Append(partial...)
	return write, diags
}

func mcpAuthModelFromClient(prior *mcpAuthModel, auth client.McpAuth) *mcpAuthModel {
	if prior == nil {
		prior = &mcpAuthModel{StaticHeaders: types.MapNull(types.StringType)}
	}
	out := &mcpAuthModel{Type: types.StringValue(auth.Type), StaticHeaders: prior.StaticHeaders}
	if auth.OAuth == nil {
		return out
	}
	priorOAuth := prior.OAuth
	if priorOAuth == nil {
		priorOAuth = &mcpOAuthModel{Scopes: types.ListNull(types.StringType)}
	}
	out.OAuth = &mcpOAuthModel{
		ClientID:     preserveEmptyString(priorOAuth.ClientID, auth.OAuth.ClientID),
		ClientSecret: priorOAuth.ClientSecret,
		TokenURL:     preserveEmptyString(priorOAuth.TokenURL, auth.OAuth.TokenURL),
		Scopes:       preserveEmptyList(priorOAuth.Scopes, auth.OAuth.Scopes),
		Issuer:       preserveEmptyString(priorOAuth.Issuer, auth.OAuth.Issuer),
		Resource:     preserveEmptyString(priorOAuth.Resource, auth.OAuth.Resource),
	}
	return out
}

func mcpOpenAPIModelFromClient(prior *mcpOpenAPIModel, openAPI *client.McpOpenAPI) *mcpOpenAPIModel {
	if openAPI == nil {
		return nil
	}
	if prior == nil {
		prior = &mcpOpenAPIModel{Operations: types.ListNull(types.StringType)}
	}
	return &mcpOpenAPIModel{
		DocumentURL: preserveEmptyString(prior.DocumentURL, openAPI.DocumentURL),
		Document:    prior.Document,
		Operations:  preserveEmptyList(prior.Operations, openAPI.Operations),
	}
}

func (r *mcpServerResource) apply(s *client.McpServer, m *mcpServerResourceModel) {
	m.ID = types.StringValue(s.ID)
	m.Key = types.StringValue(s.Key)
	m.DisplayName = types.StringValue(s.DisplayName)
	m.Description = preserveEmptyString(m.Description, s.Description)
	m.Upstream = &mcpUpstreamModel{
		Type: types.StringValue(s.Connection.Type),
		URL:  types.StringValue(s.Connection.URL),
	}
	m.Auth = mcpAuthModelFromClient(m.Auth, s.Auth)
	m.DefaultToolExposure = mcpToolExposureModelFromClient(m.DefaultToolExposure, s.DefaultToolExposure)
	m.OpenAPI = mcpOpenAPIModelFromClient(m.OpenAPI, s.OpenAPI)
	m.Sharing = mcpSharingModelFromClient(m.Sharing, s.Sharing)
	m.CreatedAt = types.StringValue(s.CreatedAt)
	m.UpdatedAt = types.StringValue(s.UpdatedAt)
}

func (r *mcpServerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan mcpServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	write, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	s, err := r.servers.Create(ctx, client.McpServerCreateInput{
		Key:                 plan.Key.ValueString(),
		DisplayName:         plan.DisplayName.ValueString(),
		Description:         strPtr(plan.Description),
		Connection:          write.connection,
		Auth:                write.auth,
		DefaultToolExposure: write.exposure,
		OpenAPI:             write.openAPI,
		Sharing:             write.sharing,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create MCP server", errDetail(err))
		return
	}

	r.apply(s, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mcpServerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state mcpServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	s, err := r.servers.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read MCP server", errDetail(err))
		return
	}

	r.apply(s, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *mcpServerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state mcpServerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	write, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	current, diags := state.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	in := client.McpServerUpdateInput{
		ID:                  plan.ID.ValueString(),
		DisplayName:         plan.DisplayName.ValueString(),
		Description:         plan.Description.ValueString(),
		DefaultToolExposure: write.exposure,
		Sharing:             write.sharing,
	}
	if write.connection != current.connection {
		in.Connection = &write.connection
	}
	if !reflect.DeepEqual(write.auth, current.auth) {
		in.Auth = &write.auth
	}
	if !reflect.DeepEqual(write.openAPI, current.openAPI) {
		in.OpenAPI = write.openAPI
	}
	s, err := r.servers.Update(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update MCP server", errDetail(err))
		return
	}

	r.apply(s, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mcpServerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state mcpServerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.servers.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete MCP server", errDetail(err))
	}
}

func (r *mcpServerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
