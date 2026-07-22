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
	_ resource.ResourceWithModifyPlan  = &policyResource{}
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
	// The server echoes evaluators in the order they were sent, so pair each one
	// with its prior/plan model to recover an elided options value. Keyed on id
	// with positional consumption among same-id evaluators (a stable tie-break
	// consistent with ModifyPlan), so duplicate ids don't all collapse onto the
	// first prior match.
	priorIdx := matchPriorByID(p.Evaluators, priorEvals)
	evs := make([]policyEvaluatorModel, 0, len(p.Evaluators))
	for i, e := range p.Evaluators {
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
			// operator's planned/prior value from the matched evaluator. An unknown
			// (create with omitted options) or an unmatched evaluator collapses to
			// null so the post-apply state never carries an unknown.
			em.Options = jsontypes.NewNormalizedNull()
			if pi := priorIdx[i]; pi >= 0 {
				if o := priorEvals[pi].Options; !o.IsUnknown() {
					em.Options = o
				}
			}
		}
		evs = append(evs, em)
	}
	m.Evaluators = evs
}

// matchPriorByID pairs each server-returned evaluator with a prior/plan evaluator
// of the same id, consuming matches so duplicate ids pair positionally (server[0]
// with the first prior of that id, server[1] with the second, ...). priorIdx[i]
// is the matched prior index for server[i], or -1 when there is no unused prior
// of that id. Keyed on id alone: the server guarantees each returned evaluator's
// execute_on, so only the operator's prior options — which id already locates —
// need recovering. The tie-break (first-unused-among-same-id) is consistent with
// ModifyPlan's identity correlation.
func matchPriorByID(server []client.EvaluatorRef, prior []policyEvaluatorModel) []int {
	byID := make(map[string][]int, len(prior))
	for i, p := range prior {
		id := p.ID.ValueString()
		byID[id] = append(byID[id], i)
	}
	cursor := make(map[string]int, len(byID))
	out := make([]int, len(server))
	for i, e := range server {
		idxs := byID[e.ID]
		if c := cursor[e.ID]; c < len(idxs) {
			out[i] = idxs[c]
			cursor[e.ID] = c + 1
		} else {
			out[i] = -1
		}
	}
	return out
}

// ModifyPlan re-correlates each planned evaluator to the prior-state evaluator
// that shares its identity so a reordered / inserted / removed evaluator block
// keeps its OWN server-derived options and is_guardrail. Terraform core merges
// nested list blocks positionally (prior[i] into plan[i]); for a swapped or
// shifted list that cross-assigns one evaluator's computed data onto another. This
// is the single mechanism that both fixes that cross-assignment and suppresses the
// phantom diff an unrelated update (e.g. a rename) would otherwise show on the
// unconfigured, wholesale-replaced evaluator fields.
func (r *policyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroy (null plan) or create (null prior state): nothing to correlate.
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state, config policyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(plan.Evaluators) == 0 {
		return
	}
	correlateEvaluatorComputed(plan.Evaluators, config.Evaluators, state.Evaluators)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// correlateEvaluatorComputed carries each matched evaluator's OWN prior value for
// a server-derived field (options, is_guardrail) into the plan, undoing Terraform
// core's positional (by-index) merge of computed nested-block attributes. It
// mutates plan in place.
//
// The identity key is (id, execute_on) — the two Required fields. Duplicate keys
// pair positionally among themselves (the first unused prior with that key pairs
// with the first plan element with that key, and so on): a stable, documented
// tie-break the server does not forbid.
//
// config[i] (NOT plan[i]) decides whether a field is unset: plan[i] may already
// carry a wrong, positionally-merged known value, so only a null CONFIG value is
// treated as unset. A matched, unset field is set to the matched prior value; an
// unmatched (new) evaluator's unset fields are reset to unknown ("known after
// apply"); an evaluator whose id is unknown (interpolated) cannot be correlated,
// so its unset fields are reset to unknown as well. Only unset (null-config)
// fields are ever touched, so AssertPlanValid never sees a non-null config value
// change.
func correlateEvaluatorComputed(plan, config, prior []policyEvaluatorModel) {
	type identity struct{ id, executeOn string }
	stateByKey := make(map[identity][]int, len(prior))
	for i, s := range prior {
		if s.ID.IsUnknown() {
			continue
		}
		k := identity{s.ID.ValueString(), s.ExecuteOn.ValueString()}
		stateByKey[k] = append(stateByKey[k], i)
	}
	used := make([]bool, len(prior))
	for i := range plan {
		var cfg policyEvaluatorModel
		if i < len(config) {
			cfg = config[i]
		}
		// resetUnset marks a field "known after apply" when config left it unset
		// and no prior value can be correlated. An explicitly configured value
		// (non-null config) is left untouched.
		resetUnset := func() {
			if cfg.Options.IsNull() {
				plan[i].Options = jsontypes.NewNormalizedUnknown()
			}
			if cfg.IsGuardrail.IsNull() {
				plan[i].IsGuardrail = types.BoolUnknown()
			}
		}
		// An unknown id (interpolated from another not-yet-applied resource) cannot
		// be correlated — never guess, leave the unset fields unknown.
		if plan[i].ID.IsUnknown() {
			resetUnset()
			continue
		}
		k := identity{plan[i].ID.ValueString(), plan[i].ExecuteOn.ValueString()}
		matched := -1
		for _, idx := range stateByKey[k] {
			if !used[idx] {
				matched = idx
				break
			}
		}
		if matched < 0 {
			resetUnset() // new evaluator: computed fields are known only after apply.
			continue
		}
		used[matched] = true
		s := prior[matched]
		// Carry the matched evaluator's OWN prior value for any field the config
		// left unset, overriding core's positional merge.
		if cfg.Options.IsNull() {
			plan[i].Options = s.Options
		}
		if cfg.IsGuardrail.IsNull() {
			plan[i].IsGuardrail = s.IsGuardrail
		}
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
	// Evaluator options / is_guardrail are Optional+Computed and the evaluator list
	// is a wholesale replace, so an unconfigured value must survive an unrelated
	// change (e.g. a rename). That retention — correlated to the prior state by
	// evaluator identity — is done in ModifyPlan, so the plan reaching here already
	// carries the retained values and the outbound request re-sends them.
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
