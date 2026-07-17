package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                = &routingRuleResource{}
	_ resource.ResourceWithConfigure   = &routingRuleResource{}
	_ resource.ResourceWithImportState = &routingRuleResource{}
)

// NewRoutingRuleResource is the factory registered on the provider.
func NewRoutingRuleResource() resource.Resource { return &routingRuleResource{} }

type routingRuleResource struct {
	rules client.RoutingRulesAPI
}

type routingRuleResourceModel struct {
	ID           types.String            `tfsdk:"id"`
	DisplayName  types.String            `tfsdk:"display_name"`
	Description  types.String            `tfsdk:"description"`
	Enabled      types.Bool              `tfsdk:"enabled"`
	ProjectID    types.String            `tfsdk:"project_id"`
	Priority     types.Int64             `tfsdk:"priority"`
	Expression   *routingExpressionModel `tfsdk:"expression"`
	ModelsConfig jsontypes.Normalized    `tfsdk:"models_config"`
	CreatedAt    types.String            `tfsdk:"created_at"`
	UpdatedAt    types.String            `tfsdk:"updated_at"`
}

type routingExpressionModel struct {
	Cel types.String `tfsdk:"cel"`
}

func (r *routingRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_routing_rule"
}

func (r *routingRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A routing rule (router entity). Carries a nullable `project_id`; because the " +
			"update API omits `project_id`, changing it forces resource replacement.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Routing rule ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable rule name.",
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Optional description.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the rule is enabled. Defaults server-side when omitted.",
			},
			"project_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Owning project. Omit for a workspace-global rule. Changing this " +
					"forces replacement (the update API does not accept `project_id`).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"priority": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Evaluation priority (>= 0). Defaults server-side when omitted.",
			},
			"expression": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Match expression. Only `cel` is writable; the server-derived `config` is not surfaced.",
				Attributes: map[string]schema.Attribute{
					"cel": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "CEL match expression.",
					},
				},
			},
			"models_config": schema.StringAttribute{
				CustomType: jsontypes.NormalizedType{},
				Optional:   true,
				MarkdownDescription: "Model routing configuration as a JSON object string " +
					"(`{\"mode\":...,\"models\":[...]}`). Compared semantically, so key order and " +
					"insignificant whitespace do not produce a diff. Preserved across updates.",
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
	}
}

func (r *routingRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.rules = c.RoutingRules()
}

func (r *routingRuleResource) apply(g *client.RoutingRule, m *routingRuleResourceModel) {
	m.ID = types.StringValue(g.ID)
	m.DisplayName = types.StringValue(g.DisplayName)
	m.Description = optString(g.Description)
	m.Enabled = types.BoolValue(g.Enabled)
	m.ProjectID = optString(g.ProjectID)
	m.Priority = types.Int64Value(g.Priority)
	if g.ExpressionCEL != "" {
		m.Expression = &routingExpressionModel{Cel: types.StringValue(g.ExpressionCEL)}
	} else {
		m.Expression = nil
	}
	m.ModelsConfig = rawToNormalized(g.ModelsConfig)
	m.CreatedAt = types.StringValue(g.CreatedAt)
	m.UpdatedAt = types.StringValue(g.UpdatedAt)
}

func (m *routingRuleResourceModel) expressionCEL() *string {
	if m.Expression == nil || m.Expression.Cel.IsNull() || m.Expression.Cel.IsUnknown() {
		return nil
	}
	s := m.Expression.Cel.ValueString()
	return &s
}

func (r *routingRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan routingRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.rules.Create(ctx, client.RoutingRuleCreateInput{
		DisplayName:   plan.DisplayName.ValueString(),
		Description:   strPtr(plan.Description),
		Enabled:       boolPtr(plan.Enabled),
		ProjectID:     strPtr(plan.ProjectID),
		Priority:      int64Ptr(plan.Priority),
		ExpressionCEL: plan.expressionCEL(),
		ModelsConfig:  normalizedToRaw(plan.ModelsConfig),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create routing rule", errDetail(err))
		return
	}
	r.apply(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *routingRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state routingRuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.rules.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read routing rule", errDetail(err))
		return
	}
	r.apply(g, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *routingRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan routingRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.DisplayName.ValueString()
	g, err := r.rules.Update(ctx, client.RoutingRuleUpdateInput{
		ID:            plan.ID.ValueString(),
		DisplayName:   &name,
		Description:   strPtr(plan.Description),
		Enabled:       boolPtr(plan.Enabled),
		Priority:      int64Ptr(plan.Priority),
		ExpressionCEL: plan.expressionCEL(),
		ModelsConfig:  normalizedToRaw(plan.ModelsConfig),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update routing rule", errDetail(err))
		return
	}
	r.apply(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *routingRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state routingRuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.rules.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete routing rule", errDetail(err))
	}
}

func (r *routingRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
