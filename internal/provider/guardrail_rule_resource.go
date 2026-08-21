package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	ID          types.String         `tfsdk:"id"`
	ExecuteOn   types.String         `tfsdk:"execute_on"`
	SampleRate  types.Float64        `tfsdk:"sample_rate"`
	IsGuardrail types.Bool           `tfsdk:"is_guardrail"`
	Options     jsontypes.Normalized `tfsdk:"options"`
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
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the rule is enabled. Defaults server-side when omitted. " +
					"An enabled rule with no `project_id` applies to EVERY inference request in the " +
					"workspace, and a failing guardrail blocks the request (HTTP 400 `guardrail_error`), " +
					"so enable deliberately.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Owning project. Omit for a workspace-global rule. Changing this " +
					"forces replacement (the update API does not accept `project_id`).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					nonEmptyStringValidator{remedy: "Omit project_id for a workspace-global rule."},
				},
			},
			"timeout": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Evaluation timeout in milliseconds. Stored and returned, but NOT " +
					"currently enforced at execution time — the effective limits come from the " +
					"grader transport and sandbox instead.",
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
							Required: true,
							MarkdownDescription: "What to run. Either a BUILT-IN guardrail slug — `orq_pii_detection` " +
								"or `orq_secret_detection` — or the id of a custom evaluator, e.g. " +
								"`orq_evaluator.my_judge.id`. The id is resolved at REQUEST time, not at " +
								"apply time: an unresolvable custom id is not rejected here, it fails every " +
								"matching inference request with a 500 once the rule is enabled.",
						},
						"execute_on": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "When to run: `input`, `output`, or `both`.",
							Validators: []validator.String{
								stringvalidator.OneOf("input", "output", "both"),
							},
						},
						"sample_rate": schema.Float64Attribute{
							Optional: true,
							MarkdownDescription: "Fraction of requests to evaluate (0-1). Omitted evaluates EVERY " +
								"matching request — the server resolves a missing sample rate to 1.0.",
						},
						"is_guardrail": schema.BoolAttribute{
							Optional: true,
							MarkdownDescription: "`true` enforces the verdict: a failing check blocks the request. " +
								"`false` observes only — the check runs asynchronously and its result lands in " +
								"traces without affecting the response. Omitted ENFORCES: the server resolves a " +
								"missing flag to `true`, so observe-only mode needs an explicit `false`. " +
								"NOTE for `python_eval` evaluators: a " +
								"boolean `false` blocks only when the evaluator itself carries a guardrail " +
								"config; `llm_eval` evaluators block on `false` without one.",
						},
						"options": schema.StringAttribute{
							CustomType: jsontypes.NormalizedType{},
							Optional:   true,
							MarkdownDescription: "Arbitrary per-guardrail configuration as a JSON object string " +
								"(e.g. PII language/threshold/entities). Compared semantically, so key order " +
								"and insignificant whitespace do not produce a diff. Preserved across updates.",
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

func guardrailRefsFromModel(models []guardrailRefModel) ([]client.GuardrailRef, diag.Diagnostics) {
	var diags diag.Diagnostics
	if models == nil {
		return nil, diags
	}
	out := make([]client.GuardrailRef, 0, len(models))
	for i, m := range models {
		ref := client.GuardrailRef{
			ID:          m.ID.ValueString(),
			ExecuteOn:   m.ExecuteOn.ValueString(),
			SampleRate:  float64Ptr(m.SampleRate),
			IsGuardrail: boolPtr(m.IsGuardrail),
		}
		// Decode the JSON-string options back into the transport-neutral map so
		// the per-guardrail config round-trips and is never dropped on update.
		if !m.Options.IsNull() && !m.Options.IsUnknown() {
			var opts map[string]any
			if err := json.Unmarshal([]byte(m.Options.ValueString()), &opts); err != nil {
				diags.AddAttributeError(
					path.Root("guardrails").AtListIndex(i).AtName("options"),
					"Invalid guardrail options",
					"options must be a JSON object: "+err.Error(),
				)
				continue
			}
			ref.Options = opts
		}
		out = append(out, ref)
	}
	return out, diags
}

func (r *guardrailRuleResource) apply(g *client.GuardrailRule, m *guardrailRuleResourceModel) {
	m.ID = types.StringValue(g.ID)
	m.DisplayName = types.StringValue(g.DisplayName)
	m.Description = preserveEmptyString(m.Description, g.Description)
	m.Enabled = types.BoolValue(g.Enabled)
	m.ProjectID = optString(g.ProjectID)
	m.Timeout = types.Int64Value(g.Timeout)
	m.CreatedAt = types.StringValue(g.CreatedAt)
	m.UpdatedAt = types.StringValue(g.UpdatedAt)

	if len(g.Guardrails) == 0 {
		m.Guardrails = nil
		return
	}
	// The server normalizes an empty options object to absent, so a planned
	// "{}" must survive the read-back or apply reports an inconsistent result.
	// The planned ref is found by POSITION — the list is compared positionally
	// anyway — and confirmed by id and phase; keying by id and phase alone made
	// two references to the same guardrail collide, restoring "{}" onto the one
	// that never asked for it.
	plannedEmptyOptions := func(i int, ref client.GuardrailRef) bool {
		if i >= len(m.Guardrails) {
			return false
		}
		prior := m.Guardrails[i]
		if prior.ID.ValueString() != ref.ID || prior.ExecuteOn.ValueString() != ref.ExecuteOn {
			return false
		}
		if prior.Options.IsNull() || prior.Options.IsUnknown() {
			return false
		}
		var opts map[string]any
		return json.Unmarshal([]byte(prior.Options.ValueString()), &opts) == nil && len(opts) == 0
	}
	refs := make([]guardrailRefModel, 0, len(g.Guardrails))
	for i, ref := range g.Guardrails {
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
		// Encode the server's options map back to a JSON string. jsontypes
		// compares semantically, so this converges with the operator's config
		// regardless of key order / whitespace.
		switch {
		case ref.Options != nil:
			if b, err := json.Marshal(ref.Options); err == nil {
				rm.Options = jsontypes.NewNormalizedValue(string(b))
			} else {
				rm.Options = jsontypes.NewNormalizedNull()
			}
		case plannedEmptyOptions(i, ref):
			rm.Options = jsontypes.NewNormalizedValue("{}")
		default:
			rm.Options = jsontypes.NewNormalizedNull()
		}
		refs = append(refs, rm)
	}
	m.Guardrails = refs
}

func (r *guardrailRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan guardrailRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	refs, refDiags := guardrailRefsFromModel(plan.Guardrails)
	resp.Diagnostics.Append(refDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.rules.Create(ctx, client.GuardrailRuleCreateInput{
		DisplayName: plan.DisplayName.ValueString(),
		Description: strPtr(plan.Description),
		Enabled:     boolPtr(plan.Enabled),
		ProjectID:   strPtr(plan.ProjectID),
		Timeout:     int64Ptr(plan.Timeout),
		Guardrails:  refs,
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
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	refs, refDiags := guardrailRefsFromModel(plan.Guardrails)
	resp.Diagnostics.Append(refDiags...)
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
		Guardrails:  refs,
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
