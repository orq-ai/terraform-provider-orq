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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                = &policyResource{}
	_ resource.ResourceWithConfigure   = &policyResource{}
	_ resource.ResourceWithImportState = &policyResource{}
)

// NewPolicyResource is the factory registered on the provider.
func NewPolicyResource() resource.Resource { return &policyResource{} }

type policyResource struct {
	policies client.PoliciesAPI
}

type policyResourceModel struct {
	ID           types.String           `tfsdk:"id"`
	DisplayName  types.String           `tfsdk:"display_name"`
	Description  types.String           `tfsdk:"description"`
	Enabled      types.Bool             `tfsdk:"enabled"`
	ProjectID    types.String           `tfsdk:"project_id"`
	Slug         types.String           `tfsdk:"slug"`
	Timeout      types.Int64            `tfsdk:"timeout"`
	Evaluators   []policyEvaluatorModel `tfsdk:"evaluators"`
	Limits       jsontypes.Normalized   `tfsdk:"limits"`
	ModelsConfig modelsConfigValue      `tfsdk:"models_config"`
	RetryConfig  retryConfigValue       `tfsdk:"retry_config"`
	CreatedAt    types.String           `tfsdk:"created_at"`
	UpdatedAt    types.String           `tfsdk:"updated_at"`
}

type policyEvaluatorModel struct {
	ID          types.String         `tfsdk:"id"`
	ExecuteOn   types.String         `tfsdk:"execute_on"`
	SampleRate  types.Float64        `tfsdk:"sample_rate"`
	IsGuardrail types.Bool           `tfsdk:"is_guardrail"`
	Options     jsontypes.Normalized `tfsdk:"options"`
}

func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy"
}

func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A policy (router entity). Carries a mutable nullable `project_id`; unlike " +
			"routing/guardrail rules, changing `project_id` updates in place (no replacement).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Policy ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable policy name.",
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
				MarkdownDescription: "Whether the policy is enabled. Defaults server-side when omitted.",
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Owning project. Omit for a workspace-global policy. Mutable (updates in place).",
			},
			"slug": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "URL-safe slug derived from the display name by orq.",
				PlanModifiers:       []planmodifier.String{policySlugPlanModifier{}},
			},
			"timeout": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Evaluation timeout in milliseconds (>= 1000). Defaults server-side when omitted.",
			},
			"limits": schema.StringAttribute{
				CustomType:          jsontypes.NormalizedType{},
				Optional:            true,
				MarkdownDescription: "Budget / request / token limits as a JSON object string. Compared semantically.",
			},
			"models_config": schema.StringAttribute{
				CustomType: modelsConfigType{},
				Optional:   true,
				Computed:   true,
				// UseStateForUnknown keeps an unrelated update (e.g. a rename) from
				// marking this Optional+Computed value unknown and churning updated_at.
				// It only ever acts on an unknown plan (null config); a non-null
				// configured value is never rewritten, so it cannot reintroduce the
				// AssertPlanValid bug the canon redesign fixed.
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Model routing configuration as a JSON object string. Compared semantically. " +
					"A model entry with an omitted or zero `weight` is stored by the server with `weight` = 0.5; " +
					"the two forms are treated as equal, so a weight-less config does not drift against the " +
					"server read-back. Optional+Computed: dropping it from config keeps the prior value.",
			},
			"retry_config": schema.StringAttribute{
				CustomType: retryConfigType{},
				Optional:   true,
				Computed:   true,
				// See models_config: unknown-only, never rewrites a non-null config.
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Retry configuration as a JSON object string (`{\"count\":...,\"on_codes\":[...]}`). " +
					"Compared semantically. An empty `on_codes: []` is elided by the server on read and is treated " +
					"as equal to an absent `on_codes`, so it does not drift. Optional+Computed: dropping it from " +
					"config keeps the prior value.",
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
			"evaluators": schema.ListNestedBlock{
				MarkdownDescription: "Referenced evaluators (mirrors guardrail refs).",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Evaluator ID.",
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
							Optional: true,
							Computed: true,
							MarkdownDescription: "Whether this reference is enforced as a guardrail (blocking). " +
								"The server stores a non-pointer bool defaulting to `false` and elides it on read; " +
								"an omitted value reads back as `false`.",
						},
						"options": schema.StringAttribute{
							CustomType: jsontypes.NormalizedType{},
							Optional:   true,
							Computed:   true,
							MarkdownDescription: "Arbitrary per-evaluator configuration as a JSON object string. " +
								"Compared semantically. The server elides an empty object entirely on read; the " +
								"provider keeps the operator's value (e.g. an explicit `{}`) in state so a non-null " +
								"config does not read back null. Preserved across updates.",
						},
					},
				},
			},
		},
	}
}

func (r *policyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.policies = c.Policies()
}

func policyEvaluatorsFromModel(models []policyEvaluatorModel) ([]client.EvaluatorRef, diag.Diagnostics) {
	var diags diag.Diagnostics
	if models == nil {
		return nil, diags
	}
	out := make([]client.EvaluatorRef, 0, len(models))
	for i, m := range models {
		ref := client.EvaluatorRef{
			ID:          m.ID.ValueString(),
			ExecuteOn:   m.ExecuteOn.ValueString(),
			SampleRate:  float64Ptr(m.SampleRate),
			IsGuardrail: boolPtr(m.IsGuardrail),
		}
		if !m.Options.IsNull() && !m.Options.IsUnknown() {
			var opts map[string]any
			if err := json.Unmarshal([]byte(m.Options.ValueString()), &opts); err != nil {
				diags.AddAttributeError(
					path.Root("evaluators").AtListIndex(i).AtName("options"),
					"Invalid evaluator options",
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

func (r *policyResource) apply(p *client.Policy, m *policyResourceModel) {
	m.ID = types.StringValue(p.ID)
	m.DisplayName = types.StringValue(p.DisplayName)
	// description is Optional+Computed: the server elides an empty description
	// (omitempty), so a "" config would otherwise read back null and error as an
	// inconsistent apply. Normalize empty to "" and let Computed absorb an
	// omitted config (prior state is retained, so no perpetual diff).
	m.Description = types.StringValue(p.Description)
	m.Enabled = types.BoolValue(p.Enabled)
	m.ProjectID = optString(p.ProjectID)
	m.Slug = types.StringValue(p.Slug)
	m.Timeout = types.Int64Value(p.Timeout)
	m.Limits = rawToNormalized(p.Limits)
	m.ModelsConfig = modelsConfigFromRaw(p.ModelsConfig)
	m.RetryConfig = retryConfigFromRaw(p.RetryConfig)
	m.CreatedAt = types.StringValue(p.CreatedAt)
	m.UpdatedAt = types.StringValue(p.UpdatedAt)

	// Capture the plan/prior evaluators (m still holds them here) so we can keep
	// the operator's options value when the server elides an empty options object.
	priorEvals := m.Evaluators

	if len(p.Evaluators) == 0 {
		m.Evaluators = nil
		return
	}
	evs := make([]policyEvaluatorModel, 0, len(p.Evaluators))
	for _, e := range p.Evaluators {
		em := policyEvaluatorModel{
			ID:        types.StringValue(e.ID),
			ExecuteOn: types.StringValue(e.ExecuteOn),
		}
		if e.SampleRate != nil {
			em.SampleRate = types.Float64Value(*e.SampleRate)
		} else {
			em.SampleRate = types.Float64Null()
		}
		// The server stores is_guardrail as a non-pointer bool defaulting to
		// false and elides it on read (omitempty), so a nil pointer means false.
		// Surfacing false (not null) keeps state == an explicit `is_guardrail =
		// false` config; the attribute is Computed so an omitted config converges.
		if e.IsGuardrail != nil {
			em.IsGuardrail = types.BoolValue(*e.IsGuardrail)
		} else {
			em.IsGuardrail = types.BoolValue(false)
		}
		if e.Options != nil {
			if b, err := json.Marshal(e.Options); err == nil {
				em.Options = jsontypes.NewNormalizedValue(string(b))
			} else {
				em.Options = jsontypes.NewNormalizedNull()
			}
		} else {
			// The server elides an empty options object entirely. A non-null config
			// options (e.g. `{}`) would otherwise read back null — a null vs
			// non-null mismatch semantic equality cannot reconcile — so keep the
			// operator's planned/prior value, matched by evaluator id (the server
			// replaces evaluators wholesale, so the read-back order equals the sent
			// order, but id-matching is robust to reorder/add/remove). An unknown
			// (create with omitted options) collapses to null.
			em.Options = priorEvaluatorOptions(priorEvals, e.ID)
		}
		evs = append(evs, em)
	}
	m.Evaluators = evs
}

// priorEvaluatorOptions returns the options value for the evaluator with the
// given id from the plan/prior-state evaluators. It returns a concrete null when
// there is no match or the prior value is unknown, so the post-apply state never
// carries an unknown.
func priorEvaluatorOptions(prior []policyEvaluatorModel, id string) jsontypes.Normalized {
	for _, p := range prior {
		if p.ID.ValueString() == id {
			if o := p.Options; !o.IsUnknown() {
				return o
			}
			return jsontypes.NewNormalizedNull()
		}
	}
	return jsontypes.NewNormalizedNull()
}

// retainEvaluatorOptions fills in each plan evaluator's options from the prior
// state when the config omitted it. options is Optional+Computed with no
// UseStateForUnknown, so an unconfigured options plans as UNKNOWN; because the
// Update replaces the evaluator list WHOLESALE, sending an evaluator without its
// options would clear options that were set on a prior apply or import. This
// mutates plan in place so both the outbound request (policyEvaluatorsFromModel)
// and the state write (apply) carry the retained value.
//
// Matching is by evaluator id, not position, so: a reordered evaluator keeps its
// own options, a removed evaluator drops entirely (its prior options are never
// looked up), and an added evaluator finds no prior and keeps its unconfigured
// (null) options. An explicitly configured options (including `{}`, which is
// known and non-unknown) is left exactly as the operator wrote it.
func retainEvaluatorOptions(plan, prior []policyEvaluatorModel) {
	if len(plan) == 0 {
		return
	}
	priorByID := make(map[string]jsontypes.Normalized, len(prior))
	for _, p := range prior {
		priorByID[p.ID.ValueString()] = p.Options
	}
	for i := range plan {
		if !plan[i].Options.IsUnknown() {
			continue // operator set it explicitly (incl. {}) — keep as-is.
		}
		if o, ok := priorByID[plan[i].ID.ValueString()]; ok && !o.IsNull() && !o.IsUnknown() {
			plan[i].Options = o
			continue
		}
		// No prior options to retain (newly added evaluator, or prior had none):
		// resolve the unknown to null so it isn't sent and never leaks into state.
		plan[i].Options = jsontypes.NewNormalizedNull()
	}
}

func (r *policyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan policyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	evs, evDiags := policyEvaluatorsFromModel(plan.Evaluators)
	resp.Diagnostics.Append(evDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.policies.Create(ctx, client.PolicyCreateInput{
		DisplayName:  plan.DisplayName.ValueString(),
		Description:  strPtr(plan.Description),
		Enabled:      boolPtr(plan.Enabled),
		ProjectID:    strPtr(plan.ProjectID),
		Timeout:      int64Ptr(plan.Timeout),
		Evaluators:   evs,
		Limits:       normalizedToRaw(plan.Limits),
		ModelsConfig: plan.ModelsConfig.toRaw(),
		RetryConfig:  plan.RetryConfig.toRaw(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create policy", errDetail(err))
		return
	}
	r.apply(p, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.policies.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read policy", errDetail(err))
		return
	}
	r.apply(p, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *policyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan policyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// options is Optional+Computed: an unconfigured options plans as unknown. The
	// evaluator list is a wholesale replace, so retain each evaluator's prior
	// options (matched by id) when the config omits it — otherwise an unrelated
	// change (e.g. a rename) would clear server-side options.
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	retainEvaluatorOptions(plan.Evaluators, state.Evaluators)
	evs, evDiags := policyEvaluatorsFromModel(plan.Evaluators)
	resp.Diagnostics.Append(evDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Removing every evaluator block must clear them server-side. The server
	// fully replaces evaluators when the field is present (policies/routes.go),
	// so send an explicit empty (non-nil) slice — a nil would omit the field and
	// leave the prior evaluators in place, drifting forever.
	if evs == nil {
		evs = []client.EvaluatorRef{}
	}
	name := plan.DisplayName.ValueString()
	// project_id is mutable and clearable: send it explicitly (empty when the
	// config omits it) so dropping project_id reverts the policy to
	// workspace-global instead of retaining the server's prior value.
	projectID := plan.ProjectID.ValueString()
	p, err := r.policies.Update(ctx, client.PolicyUpdateInput{
		ID:           plan.ID.ValueString(),
		DisplayName:  &name,
		Description:  strPtr(plan.Description),
		Enabled:      boolPtr(plan.Enabled),
		ProjectID:    &projectID,
		Timeout:      int64Ptr(plan.Timeout),
		Evaluators:   evs,
		Limits:       normalizedToRaw(plan.Limits),
		ModelsConfig: plan.ModelsConfig.toRaw(),
		RetryConfig:  plan.RetryConfig.toRaw(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to update policy", errDetail(err))
		return
	}
	r.apply(p, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state policyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.policies.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete policy", errDetail(err))
	}
}

func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// policySlugPlanModifier keeps the prior slug while `display_name` is unchanged
// and marks slug unknown ("known after apply") when `display_name` changes.
// UseStateForUnknown is wrong here: the server RE-DERIVES slug from display_name
// on update, so keeping the stale slug across a rename produces "inconsistent
// result after apply: .slug" (mirrors projectKeyPlanModifier for orq_project.key).
type policySlugPlanModifier struct{}

func (policySlugPlanModifier) Description(context.Context) string {
	return "keep the prior slug while display_name is unchanged; mark slug unknown when display_name changes"
}
func (m policySlugPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}
func (policySlugPlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Create: no prior state — the slug is known only after apply.
	if req.State.Raw.IsNull() {
		resp.PlanValue = types.StringUnknown()
		return
	}
	var planName, stateName types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("display_name"), &planName)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("display_name"), &stateName)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.PlanValue = policySlugPlanValue(planName, stateName, req.StateValue)
}

// policySlugPlanValue is the pure core: keep the prior slug while display_name is
// unchanged, else mark it unknown (the server re-derives slug from display_name).
func policySlugPlanValue(planName, stateName, priorSlug types.String) types.String {
	if planName.Equal(stateName) {
		return priorSlug
	}
	return types.StringUnknown()
}
