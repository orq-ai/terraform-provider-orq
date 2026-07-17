package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                = &guardrailRuleResource{}
	_ resource.ResourceWithConfigure   = &guardrailRuleResource{}
	_ resource.ResourceWithImportState = &guardrailRuleResource{}
)

// NewGuardrailRuleResource is the factory registered on the provider.
func NewGuardrailRuleResource() resource.Resource { return &guardrailRuleResource{} }

type guardrailRuleResource struct {
	rules client.GuardrailRulesAPI
}

type guardrailRuleResourceModel struct {
	ID          types.String        `tfsdk:"id"`
	DisplayName types.String        `tfsdk:"display_name"`
	Description types.String        `tfsdk:"description"`
	Enabled     types.Bool          `tfsdk:"enabled"`
	ProjectID   types.String        `tfsdk:"project_id"`
	Timeout     types.Int64         `tfsdk:"timeout"`
	Guardrails  []guardrailRefModel `tfsdk:"guardrails"`
	CreatedAt   types.String        `tfsdk:"created_at"`
	UpdatedAt   types.String        `tfsdk:"updated_at"`
}

type guardrailRefModel struct {
	ID          types.String  `tfsdk:"id"`
	ExecuteOn   types.String  `tfsdk:"execute_on"`
	SampleRate  types.Float64 `tfsdk:"sample_rate"`
	IsGuardrail types.Bool    `tfsdk:"is_guardrail"`
}

func (r *guardrailRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_guardrail_rule"
}

func (r *guardrailRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A guardrail rule (router entity). Carries a nullable `project_id`; because the " +
			"update API omits `project_id`, changing it forces resource replacement.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Guardrail rule ID assigned by orq.",
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
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Owning project. Omit for a workspace-global rule. Changing this " +
					"forces replacement (the update API does not accept `project_id`).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"timeout": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Evaluation timeout in milliseconds.",
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
				MarkdownDescription: "Referenced guardrail evaluators.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Guardrail evaluator ID.",
						},
						"execute_on": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "When to run: `input`, `output`, or `both`.",
							Validators: []validator.String{
								stringvalidator.OneOf("input", "output", "both"),
							},
						},
						"sample_rate": schema.Float64Attribute{
							Optional:            true,
							MarkdownDescription: "Fraction of requests to evaluate (0-1).",
						},
						"is_guardrail": schema.BoolAttribute{
							Optional:            true,
							MarkdownDescription: "Whether this reference is enforced as a guardrail (blocking).",
						},
					},
				},
			},
		},
	}
}

func (r *guardrailRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.rules = c.GuardrailRules()
}

func guardrailRefsFromModel(models []guardrailRefModel) []client.GuardrailRef {
	if models == nil {
		return nil
	}
	out := make([]client.GuardrailRef, 0, len(models))
	for _, m := range models {
		out = append(out, client.GuardrailRef{
			ID:          m.ID.ValueString(),
			ExecuteOn:   m.ExecuteOn.ValueString(),
			SampleRate:  float64Ptr(m.SampleRate),
			IsGuardrail: boolPtr(m.IsGuardrail),
		})
	}
	return out
}

func (r *guardrailRuleResource) apply(g *client.GuardrailRule, m *guardrailRuleResourceModel) {
	m.ID = types.StringValue(g.ID)
	m.DisplayName = types.StringValue(g.DisplayName)
	m.Description = optString(g.Description)
	m.Enabled = types.BoolValue(g.Enabled)
	m.ProjectID = optString(g.ProjectID)
	m.Timeout = types.Int64Value(g.Timeout)
	m.CreatedAt = types.StringValue(g.CreatedAt)
	m.UpdatedAt = types.StringValue(g.UpdatedAt)

	if len(g.Guardrails) == 0 {
		m.Guardrails = nil
		return
	}
	refs := make([]guardrailRefModel, 0, len(g.Guardrails))
	for _, ref := range g.Guardrails {
		rm := guardrailRefModel{
			ID:        types.StringValue(ref.ID),
			ExecuteOn: types.StringValue(ref.ExecuteOn),
		}
		if ref.SampleRate != nil {
			rm.SampleRate = types.Float64Value(*ref.SampleRate)
		} else {
			rm.SampleRate = types.Float64Null()
		}
		if ref.IsGuardrail != nil {
			rm.IsGuardrail = types.BoolValue(*ref.IsGuardrail)
		} else {
			rm.IsGuardrail = types.BoolNull()
		}
		refs = append(refs, rm)
	}
	m.Guardrails = refs
}

func (r *guardrailRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan guardrailRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	g, err := r.rules.Create(ctx, client.GuardrailRuleCreateInput{
		DisplayName: plan.DisplayName.ValueString(),
		Description: strPtr(plan.Description),
		Enabled:     boolPtr(plan.Enabled),
		ProjectID:   strPtr(plan.ProjectID),
		Timeout:     int64Ptr(plan.Timeout),
		Guardrails:  guardrailRefsFromModel(plan.Guardrails),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create guardrail rule", errDetail(err))
		return
	}

	r.apply(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *guardrailRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state guardrailRuleResourceModel
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
		resp.Diagnostics.AddError("Unable to read guardrail rule", errDetail(err))
		return
	}

	r.apply(g, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *guardrailRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan guardrailRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.DisplayName.ValueString()
	g, err := r.rules.Update(ctx, client.GuardrailRuleUpdateInput{
		ID:          plan.ID.ValueString(),
		DisplayName: &name,
		Description: strPtr(plan.Description),
		Enabled:     boolPtr(plan.Enabled),
		Timeout:     int64Ptr(plan.Timeout),
		Guardrails:  guardrailRefsFromModel(plan.Guardrails),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update guardrail rule", errDetail(err))
		return
	}

	r.apply(g, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *guardrailRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state guardrailRuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.rules.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete guardrail rule", errDetail(err))
	}
}

func (r *guardrailRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
