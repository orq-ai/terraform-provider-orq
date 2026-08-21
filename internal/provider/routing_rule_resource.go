package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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
	ID           types.String                  `tfsdk:"id"`
	DisplayName  types.String                  `tfsdk:"display_name"`
	Description  types.String                  `tfsdk:"description"`
	Enabled      types.Bool                    `tfsdk:"enabled"`
	ProjectID    types.String                  `tfsdk:"project_id"`
	Priority     types.Int64                   `tfsdk:"priority"`
	Expression   *routingExpressionModel       `tfsdk:"expression"`
	ModelsConfig *routingRuleModelsConfigModel `tfsdk:"models_config"`
	CreatedAt    types.String                  `tfsdk:"created_at"`
	UpdatedAt    types.String                  `tfsdk:"updated_at"`
}

type routingExpressionModel struct {
	Cel types.String `tfsdk:"cel"`
}

type routingRuleModelsConfigModel struct {
	Mode   types.String               `tfsdk:"mode"`
	Models []routingRuleModelRefModel `tfsdk:"models"`
}

type routingRuleModelRefModel struct {
	Model         types.String  `tfsdk:"model"`
	DisplayName   types.String  `tfsdk:"display_name"`
	Weight        types.Float64 `tfsdk:"weight"`
	IntegrationID types.String  `tfsdk:"integration_id"`
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
			"models_config": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Model routing configuration. Omit it entirely for a rule that only " +
					"matches; removing the block from a managed rule clears it server-side.",
				Attributes: map[string]schema.Attribute{
					"mode": schema.StringAttribute{
						Required: true,
						MarkdownDescription: "Load-balancing mode across `models`: `fallback`, `latency_based`, " +
							"`weighted`, or `round_robin`.",
						Validators: []validator.String{
							stringvalidator.OneOf("fallback", "latency_based", "weighted", "round_robin"),
						},
					},
					"models": schema.ListNestedAttribute{
						Required:            true,
						MarkdownDescription: "Candidate models, in fallback order. At least one is required.",
						Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"model": schema.StringAttribute{
									Required:            true,
									MarkdownDescription: "Model reference, e.g. `openai/gpt-4o`.",
									Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
								},
								"display_name": schema.StringAttribute{
									Optional:            true,
									Computed:            true,
									MarkdownDescription: "Label shown in the routing UI. Omitted stores an empty label.",
								},
								"weight": schema.Float64Attribute{
									Optional: true,
									Computed: true,
									MarkdownDescription: "Share of traffic for `weighted` mode. Omitted stores the " +
										"server default of `0.5`. `0` is rejected — at plan time, and again before " +
										"the write when the value is only known then — because the server rewrites " +
										"it to `0.5`, which would make the apply inconsistent.",
									Validators: []validator.Float64{float64validator.Between(0.001, 1)},
								},
								"integration_id": schema.StringAttribute{
									Optional:            true,
									Computed:            true,
									MarkdownDescription: "Integration to serve this model through. Omitted stores none.",
								},
							},
						},
					},
				},
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
	// description is Optional+Computed: the server elides an empty description
	// (omitempty), so a "" config would otherwise read back null and error as an
	// inconsistent apply. Normalize empty to "" and let Computed absorb an
	// omitted config (prior state is retained, so no perpetual diff).
	m.Description = types.StringValue(g.Description)
	m.Enabled = types.BoolValue(g.Enabled)
	m.ProjectID = optString(g.ProjectID)
	m.Priority = types.Int64Value(g.Priority)
	if g.ExpressionCEL != "" {
		m.Expression = &routingExpressionModel{Cel: types.StringValue(g.ExpressionCEL)}
	} else {
		m.Expression = nil
	}
	m.ModelsConfig = modelsConfigModel(g.ModelsConfig)
	m.CreatedAt = types.StringValue(g.CreatedAt)
	m.UpdatedAt = types.StringValue(g.UpdatedAt)
}

// modelsConfigModel is the authoritative read-back: every leaf comes from the
// server, including the defaults it fills in for an omitted display_name,
// weight, or integration_id (which is why those three are Optional+Computed).
func modelsConfigModel(c *client.RoutingRuleModelsConfig) *routingRuleModelsConfigModel {
	if c == nil {
		return nil
	}
	out := &routingRuleModelsConfigModel{
		Mode:   types.StringValue(c.Mode),
		Models: make([]routingRuleModelRefModel, 0, len(c.Models)),
	}
	for _, m := range c.Models {
		out.Models = append(out.Models, routingRuleModelRefModel{
			Model:         types.StringValue(m.Model),
			DisplayName:   types.StringValue(m.DisplayName),
			Weight:        optFloat64Ptr(m.Weight),
			IntegrationID: types.StringValue(m.IntegrationID),
		})
	}
	return out
}

// modelsConfigInput builds the write shape. A nil model means the block is
// absent from config: the write omits the field entirely (the server rejects an
// explicit null) and an update clears any stored config instead.
func (m *routingRuleModelsConfigModel) modelsConfigInput() *client.RoutingRuleModelsConfig {
	if m == nil {
		return nil
	}
	out := &client.RoutingRuleModelsConfig{
		Mode:   m.Mode.ValueString(),
		Models: make([]client.RoutingRuleModelRef, 0, len(m.Models)),
	}
	for _, e := range m.Models {
		out.Models = append(out.Models, client.RoutingRuleModelRef{
			Model:         e.Model.ValueString(),
			DisplayName:   e.DisplayName.ValueString(),
			Weight:        float64Ptr(e.Weight),
			IntegrationID: e.IntegrationID.ValueString(),
		})
	}
	return out
}

// validateWeights re-checks the one weight the server silently rewrites. The
// schema validator only sees values known at plan time, so an interpolated
// weight that resolves to 0 reaches apply unchecked: the server would store 0.5,
// contradict the plan, and leave a created-but-tainted rule. Called before the
// write, so nothing is created.
func (m *routingRuleModelsConfigModel) validateWeights() diag.Diagnostics {
	var diags diag.Diagnostics
	if m == nil {
		return diags
	}
	for i, e := range m.Models {
		if e.Weight.IsNull() || e.Weight.IsUnknown() || e.Weight.ValueFloat64() != 0 {
			continue
		}
		diags.AddAttributeError(
			path.Root("models_config").AtName("models").AtListIndex(i).AtName("weight"),
			"Invalid model weight",
			"weight must be greater than 0 (the accepted range is 0.001 to 1). The server rewrites a "+
				"weight of 0 to its 0.5 default, which would contradict the plan and taint the rule. "+
				"Omit weight to take the default explicitly.",
		)
	}
	return diags
}

func (m *routingRuleResourceModel) expressionCEL() *string {
	if m.Expression == nil || m.Expression.Cel.IsNull() || m.Expression.Cel.IsUnknown() {
		return nil
	}
	s := m.Expression.Cel.ValueString()
	return &s
}

// expressionCELForUpdate returns the CEL to send on a sparse update. Unlike
// create, a removed expression sends an explicit empty string: the server
// unsets the expression when it receives `expression.cel == ""`
// (routingrules/routes.go), which is the only way a PATCH can clear it. Without
// this, dropping the block would leave the server's prior expression in place
// and drift forever.
func (m *routingRuleResourceModel) expressionCELForUpdate() *string {
	if s := m.expressionCEL(); s != nil {
		return s
	}
	empty := ""
	return &empty
}

func (r *routingRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan routingRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	resp.Diagnostics.Append(plan.ModelsConfig.validateWeights()...)
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
		ModelsConfig:  plan.ModelsConfig.modelsConfigInput(),
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
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	resp.Diagnostics.Append(plan.ModelsConfig.validateWeights()...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.DisplayName.ValueString()
	modelsConfig := plan.ModelsConfig.modelsConfigInput()
	g, err := r.rules.Update(ctx, client.RoutingRuleUpdateInput{
		ID:                plan.ID.ValueString(),
		DisplayName:       &name,
		Description:       strPtr(plan.Description),
		Enabled:           boolPtr(plan.Enabled),
		Priority:          int64Ptr(plan.Priority),
		ExpressionCEL:     plan.expressionCELForUpdate(),
		ModelsConfig:      modelsConfig,
		ClearModelsConfig: modelsConfig == nil,
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
