package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                = &mcpGatewayResource{}
	_ resource.ResourceWithConfigure   = &mcpGatewayResource{}
	_ resource.ResourceWithImportState = &mcpGatewayResource{}
)

func NewMcpGatewayResource() resource.Resource { return &mcpGatewayResource{} }

type mcpGatewayResource struct {
	gateways client.McpGatewaysAPI
}

type mcpGatewayResourceModel struct {
	ID          types.String         `tfsdk:"id"`
	Key         types.String         `tfsdk:"key"`
	DisplayName types.String         `tfsdk:"display_name"`
	Description types.String         `tfsdk:"description"`
	Mode        types.String         `tfsdk:"mode"`
	ToolNaming  types.String         `tfsdk:"tool_naming"`
	WebsiteURL  types.String         `tfsdk:"website_url"`
	ServerLinks []mcpServerLinkModel `tfsdk:"server_links"`
	Guardrails  []guardrailRefModel  `tfsdk:"guardrails"`
	Sharing     *mcpSharingModel     `tfsdk:"sharing"`
	PublicURL   types.String         `tfsdk:"public_url"`
	CreatedAt   types.String         `tfsdk:"created_at"`
	UpdatedAt   types.String         `tfsdk:"updated_at"`
}

type mcpServerLinkModel struct {
	McpServerID   types.String           `tfsdk:"mcp_server_id"`
	Alias         types.String           `tfsdk:"alias"`
	Enabled       types.Bool             `tfsdk:"enabled"`
	ToolExposure  *mcpToolExposureModel  `tfsdk:"tool_exposure"`
	SkillExposure *mcpSkillExposureModel `tfsdk:"skill_exposure"`
}

type mcpSkillExposureModel struct {
	Mode      types.String `tfsdk:"mode"`
	SkillURIs types.List   `tfsdk:"skill_uris"`
}

func (r *mcpGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mcp_gateway"
}

func (r *mcpGatewayResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An MCP gateway that exposes a curated tool surface of upstream MCP servers to MCP " +
			"clients. Plugins, served workspace skills, icons, runtime limits and per-link resource exposure " +
			"are not managed by this resource and are left as configured in orq.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "MCP gateway ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"key": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Lowercase slug (letters, digits, hyphens, underscores; max 64), unique per " +
					"workspace. Backs the gateway's public MCP URL. Immutable: changing it forces replacement.",
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
				MarkdownDescription: "Free-form note about what this gateway is for.",
			},
			"mode": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(client.McpGatewayModeCode),
				MarkdownDescription: "Shape of the tool surface: `CODE` (compact discovery-and-execution surface) or " +
					"`DIRECT` (each upstream tool exposed directly). Defaults to `CODE`.",
				Validators: []validator.String{stringvalidator.OneOf(client.McpGatewayModeCode, client.McpGatewayModeDirect)},
			},
			"tool_naming": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(client.McpToolNamingPrefixOnCollision),
				MarkdownDescription: "How upstream tool names are namespaced: `PREFIX_WITH_SERVER_KEY` or " +
					"`PREFIX_ON_COLLISION`. Defaults to `PREFIX_ON_COLLISION`.",
				Validators: []validator.String{stringvalidator.OneOf(
					client.McpToolNamingPrefixWithServerKey, client.McpToolNamingPrefixOnCollision,
				)},
			},
			"website_url": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Website advertised to MCP clients. Omitted advertises the orq documentation.",
				Validators: []validator.String{
					nonEmptyStringValidator{remedy: "Omit website_url to advertise the default website."},
				},
			},
			"server_links": schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "Upstream servers this gateway aggregates; omitted removes every link. A server " +
					"can only be linked once its tools were synced, and the gateway's `sharing` must stay within " +
					"each linked server's `sharing`.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"mcp_server_id": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "ID of the upstream MCP server, e.g. `orq_mcp_server.docs.id`.",
							Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"alias": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Prefix used instead of the server key when namespacing this server's " +
								"tool names. Unique within the gateway.",
							Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"enabled": schema.BoolAttribute{
							Optional:            true,
							Computed:            true,
							Default:             booldefault.StaticBool(true),
							MarkdownDescription: "Whether this link contributes tools to the gateway. Defaults to `true`.",
						},
						"tool_exposure": schema.SingleNestedAttribute{
							Required: true,
							MarkdownDescription: "Which of the server's tools this gateway exposes. `tool_ids` and " +
								"`excluded_tool_ids` are only accepted when `mode` is `SELECTED`.",
							Attributes: mcpToolExposureAttributes(),
						},
						"skill_exposure": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Which of the server's skills this gateway serves. Omitted serves none.",
							Attributes: map[string]schema.Attribute{
								"mode": schema.StringAttribute{
									Required:            true,
									MarkdownDescription: "Selection strategy: `ALL`, `SELECTED` or `NONE`.",
									Validators:          []validator.String{stringvalidator.OneOf(mcpExposureModes...)},
								},
								"skill_uris": schema.ListAttribute{
									Optional:            true,
									ElementType:         types.StringType,
									MarkdownDescription: "Skill URIs to serve when `mode` is `SELECTED`.",
								},
							},
						},
					},
				},
			},
			"sharing": mcpSharingAttribute("gateway"),
			"public_url": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Endpoint MCP clients connect to; relative when the deployment has no public " +
					"base URL configured.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
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
		Blocks: map[string]schema.Block{
			"guardrails": schema.ListNestedBlock{
				MarkdownDescription: "Guardrails run on the arguments and result of every tool call this gateway serves. " +
					"Omitting every block removes the gateway's guardrails.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "A built-in guardrail slug (`orq_pii_detection`, `orq_secret_detection`) " +
								"or the id of a custom evaluator.",
						},
						"execute_on": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "When to run: `input`, `output`, or `both`.",
							Validators:          []validator.String{stringvalidator.OneOf("input", "output", "both")},
						},
						"sample_rate": schema.Float64Attribute{
							Optional:            true,
							MarkdownDescription: "Fraction of tool calls to evaluate (0-1).",
						},
						"is_guardrail": schema.BoolAttribute{
							Optional:            true,
							MarkdownDescription: "`true` blocks the tool call on a failing check; `false` only observes.",
						},
						"options": schema.StringAttribute{
							CustomType:          jsontypes.NormalizedType{},
							Optional:            true,
							MarkdownDescription: "Per-guardrail configuration as a JSON object string.",
						},
					},
				},
			},
		},
	}
}

func (r *mcpGatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.gateways = c.McpGateways()
}

func mcpServerLinksFromModel(ctx context.Context, models []mcpServerLinkModel) ([]client.McpGatewayServerLink, diag.Diagnostics) {
	var diags diag.Diagnostics
	links := make([]client.McpGatewayServerLink, 0, len(models))
	for _, m := range models {
		toolExposure, exposureDiags := m.ToolExposure.toClient(ctx)
		diags.Append(exposureDiags...)
		link := client.McpGatewayServerLink{
			McpServerID:  m.McpServerID.ValueString(),
			Alias:        m.Alias.ValueString(),
			Enabled:      m.Enabled.ValueBool(),
			ToolExposure: toolExposure,
		}
		if m.SkillExposure != nil {
			skillURIs, skillDiags := stringSlice(ctx, m.SkillExposure.SkillURIs)
			diags.Append(skillDiags...)
			link.SkillExposure = &client.McpSkillExposure{Mode: m.SkillExposure.Mode.ValueString(), SkillURIs: skillURIs}
		}
		links = append(links, link)
	}
	return links, diags
}

func mcpServerLinkModels(prior []mcpServerLinkModel, links []client.McpGatewayServerLink) []mcpServerLinkModel {
	if len(links) == 0 && prior == nil {
		return nil
	}
	models := make([]mcpServerLinkModel, 0, len(links))
	for i, link := range links {
		var priorLink mcpServerLinkModel
		if i < len(prior) && prior[i].McpServerID.ValueString() == link.McpServerID {
			priorLink = prior[i]
		}
		models = append(models, mcpServerLinkModel{
			McpServerID:   types.StringValue(link.McpServerID),
			Alias:         types.StringValue(link.Alias),
			Enabled:       types.BoolValue(link.Enabled),
			ToolExposure:  mcpToolExposureModelFromClient(priorLink.ToolExposure, link.ToolExposure),
			SkillExposure: mcpSkillExposureModelFromClient(priorLink.SkillExposure, link.SkillExposure),
		})
	}
	return models
}

func mcpSkillExposureModelFromClient(prior *mcpSkillExposureModel, exposure *client.McpSkillExposure) *mcpSkillExposureModel {
	if exposure == nil {
		return nil
	}
	priorURIs := types.ListNull(types.StringType)
	if prior != nil {
		priorURIs = prior.SkillURIs
	}
	return &mcpSkillExposureModel{
		Mode:      types.StringValue(exposure.Mode),
		SkillURIs: preserveEmptyList(priorURIs, exposure.SkillURIs),
	}
}

type mcpGatewayWrite struct {
	links      []client.McpGatewayServerLink
	guardrails []client.GuardrailRef
	sharing    *client.McpSharing
}

func (m *mcpGatewayResourceModel) toWrite(ctx context.Context) (mcpGatewayWrite, diag.Diagnostics) {
	var diags diag.Diagnostics
	var partial diag.Diagnostics
	var write mcpGatewayWrite
	write.links, partial = mcpServerLinksFromModel(ctx, m.ServerLinks)
	diags.Append(partial...)
	write.guardrails, partial = guardrailRefsFromModel(m.Guardrails)
	diags.Append(partial...)
	write.sharing, partial = m.Sharing.toClient(ctx)
	diags.Append(partial...)
	return write, diags
}

func (r *mcpGatewayResource) apply(g *client.McpGateway, m *mcpGatewayResourceModel) {
	m.ID = types.StringValue(g.ID)
	m.Key = types.StringValue(g.Key)
	m.DisplayName = types.StringValue(g.DisplayName)
	m.Description = preserveEmptyString(m.Description, g.Description)
	m.Mode = types.StringValue(g.Mode)
	m.ToolNaming = types.StringValue(g.ToolNaming)
	m.WebsiteURL = optString(g.WebsiteURL)
	m.ServerLinks = mcpServerLinkModels(m.ServerLinks, g.ServerLinks)
	m.Guardrails = guardrailRefModels(m.Guardrails, g.Guardrails)
	m.Sharing = mcpSharingModelFromClient(m.Sharing, g.Sharing)
	m.PublicURL = types.StringValue(g.PublicURL)
	m.CreatedAt = types.StringValue(g.CreatedAt)
	m.UpdatedAt = types.StringValue(g.UpdatedAt)
}

func (r *mcpGatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan mcpGatewayResourceModel
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

	g, err := r.gateways.Create(ctx, client.McpGatewayCreateInput{
		Key:         plan.Key.ValueString(),
		DisplayName: plan.DisplayName.ValueString(),
		Description: strPtr(plan.Description),
		Mode:        plan.Mode.ValueString(),
		ToolNaming:  plan.ToolNaming.ValueString(),
		WebsiteURL:  plan.WebsiteURL.ValueString(),
		ServerLinks: write.links,
		Guardrails:  write.guardrails,
		Sharing:     write.sharing,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create MCP gateway", errDetail(err))
		return
	}

	r.apply(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mcpGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state mcpGatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	g, err := r.gateways.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read MCP gateway", errDetail(err))
		return
	}

	r.apply(g, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *mcpGatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan mcpGatewayResourceModel
	var observedUpdatedAt types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("updated_at"), &observedUpdatedAt)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	write, diags := plan.toWrite(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	g, err := r.gateways.Update(ctx, client.McpGatewayUpdateInput{
		ID:                plan.ID.ValueString(),
		DisplayName:       plan.DisplayName.ValueString(),
		Description:       plan.Description.ValueString(),
		Mode:              plan.Mode.ValueString(),
		ToolNaming:        plan.ToolNaming.ValueString(),
		WebsiteURL:        plan.WebsiteURL.ValueString(),
		ServerLinks:       write.links,
		Guardrails:        write.guardrails,
		Sharing:           write.sharing,
		ExpectedUpdatedAt: observedUpdatedAt.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update MCP gateway", errDetail(err))
		return
	}

	r.apply(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mcpGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state mcpGatewayResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.gateways.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete MCP gateway", errDetail(err))
	}
}

func (r *mcpGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
