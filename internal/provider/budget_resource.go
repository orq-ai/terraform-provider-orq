package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                   = &budgetResource{}
	_ resource.ResourceWithConfigure      = &budgetResource{}
	_ resource.ResourceWithImportState    = &budgetResource{}
	_ resource.ResourceWithModifyPlan     = &budgetResource{}
	_ resource.ResourceWithValidateConfig = &budgetResource{}
)

// NewBudgetResource is the factory registered on the provider.
func NewBudgetResource() resource.Resource { return &budgetResource{} }

type budgetResource struct {
	budgets client.BudgetsAPI
}

type budgetResourceModel struct {
	ID              types.String       `tfsdk:"id"`
	Scope           *budgetScopeModel  `tfsdk:"scope"`
	MatchCEL        types.String       `tfsdk:"match_cel"`
	Limits          *budgetLimitsModel `tfsdk:"limits"`
	RateLimitPerMin types.Int64        `tfsdk:"rate_limit_per_minute"`
	IsActive        types.Bool         `tfsdk:"is_active"`
	ExpiresAt       rfc3339Instant     `tfsdk:"expires_at"`
	Alerts          []budgetAlertModel `tfsdk:"alerts"`
	CreatedAt       types.String       `tfsdk:"created_at"`
	UpdatedAt       types.String       `tfsdk:"updated_at"`
}

type budgetScopeModel struct {
	Kind   types.String `tfsdk:"kind"`
	Target types.String `tfsdk:"target"`
}

type budgetLimitsModel struct {
	Period     types.String  `tfsdk:"period"`
	Amount     types.Float64 `tfsdk:"amount"`
	TokenLimit types.Float64 `tfsdk:"token_limit"`
}

type budgetAlertModel struct {
	ID               types.String `tfsdk:"id"`
	ThresholdPercent types.Int64  `tfsdk:"threshold_percent"`
	NotifierIDs      types.List   `tfsdk:"notifier_ids"`
	Dimension        types.String `tfsdk:"dimension"`
}

func (r *budgetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_budget"
}

func (r *budgetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A budget governing spend, token usage, or request rate. Provide exactly one of a " +
			"structured `scope` (immutable) or a `match_cel` expression.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Budget ID assigned by orq.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"scope": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Structured scope. The scope is immutable — changing it forces replacement. " +
					"Mutually exclusive with `match_cel`.",
				PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "One of `WORKSPACE`, `PROJECT`, `IDENTITY`, `API_KEY`, `PROVIDER`, `MODEL`.",
						Validators: []validator.String{
							stringvalidator.OneOf(
								client.BudgetScopeWorkspace, client.BudgetScopeProject, client.BudgetScopeIdentity,
								client.BudgetScopeAPIKey, client.BudgetScopeProvider, client.BudgetScopeModel,
							),
						},
					},
					"target": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Scope target (project ID, identity external ID, api-key ID, provider, " +
							"or model reference). Omit for `WORKSPACE`.",
						Validators: []validator.String{
							nonEmptyStringValidator{remedy: "Omit target for a WORKSPACE scope."},
						},
					},
				},
			},
			"match_cel": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Raw CEL matching expression for a dynamic budget. Mutually exclusive with `scope`.",
				Validators: []validator.String{
					nonEmptyStringValidator{remedy: "An empty expression matches everything; use a scope block instead."},
				},
			},
			"limits": schema.SingleNestedAttribute{
				Required:            true,
				MarkdownDescription: "Per-period spend and token ceilings. At least one of `amount`, `token_limit`, or `rate_limit_per_minute` must be set.",
				Attributes: map[string]schema.Attribute{
					"period": schema.StringAttribute{
						Required: true,
						MarkdownDescription: "Rollover cadence: `DAILY`, `WEEKLY`, `MONTHLY`, `YEARLY`, or `ONE_TIME`. " +
							"Required: the server has no default, and any other value is rejected at plan time.",
						Validators: []validator.String{
							stringvalidator.OneOf("DAILY", "WEEKLY", "MONTHLY", "YEARLY", "ONE_TIME"),
						},
					},
					"amount": schema.Float64Attribute{
						Optional:            true,
						MarkdownDescription: "Spend ceiling in USD.",
					},
					"token_limit": schema.Float64Attribute{
						Optional:            true,
						MarkdownDescription: "Token ceiling.",
					},
				},
			},
			"rate_limit_per_minute": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Per-minute request ceiling.",
				Validators:          []validator.Int64{int64validator.AtLeast(1)},
			},
			"is_active": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Whether the budget is active. Defaults to true.",
			},
			"expires_at": schema.StringAttribute{
				CustomType:          rfc3339InstantType{},
				Optional:            true,
				MarkdownDescription: "Optional expiration (RFC 3339). Must be in the future when the budget is active. Compared as an instant, so an equivalent value in a different UTC offset does not produce a diff.",
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
			"alerts": schema.ListNestedBlock{
				MarkdownDescription: "Threshold notifications. Each fires once per period when consumption crosses the threshold.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Alert ID assigned by orq.",
							PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
						},
						"threshold_percent": schema.Int64Attribute{
							Required:            true,
							MarkdownDescription: "Percentage of the dimension's limit at which to fire (1-100).",
							Validators:          []validator.Int64{int64validator.Between(1, 100)},
						},
						"notifier_ids": schema.ListAttribute{
							Required:            true,
							ElementType:         types.StringType,
							MarkdownDescription: "Workspace-scoped notifier IDs to notify (1-10).",
						},
						"dimension": schema.StringAttribute{
							Optional: true,
							Computed: true,
							MarkdownDescription: "Which limit the threshold applies to: `COST` (default) or `TOKENS`. " +
								"Note: current servers may reject `TOKENS` (\"the TOKENS dimension is not supported yet\"); " +
								"the validator still accepts it in anticipation of server support.",
							Validators: []validator.String{stringvalidator.OneOf("COST", "TOKENS")},
						},
					},
				},
			},
		},
	}
}

func (r *budgetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.budgets = c.Budgets()
}

// ValidateConfig enforces the scope XOR match_cel invariant (the server also
// enforces it, but a plan-time error is clearer).
func (r *budgetResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg budgetResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateBudgetScopeXOR(cfg.Scope, cfg.MatchCEL)...)
}

// validateBudgetScopeXOR enforces the scope-XOR-match_cel invariant. Pure so it
// is unit-testable without a tfsdk.Config, and is called from BOTH ValidateConfig
// and the top of Create/Update.
//
// An UNKNOWN match_cel (interpolated from a not-yet-applied resource) counts as
// "possibly present", so the XOR cannot be decided yet: defer (no diagnostics). The
// framework invokes ValidateConfig ONLY at validate/plan time and does NOT re-run it
// at apply, so this deferred invariant would otherwise never be enforced for an
// interpolated match_cel; the Create/Update recheck (where match_cel is known) is the
// actual enforcement. Treating unknown as absent would wrongly reject a valid
// scope-less config whose match_cel is only known after apply. (The competing
// selector, `scope`, is a structural block whose presence is always known at plan
// time, so it has no unknown case to guard here.)
func validateBudgetScopeXOR(scope *budgetScopeModel, matchCEL types.String) diag.Diagnostics {
	var diags diag.Diagnostics
	if matchCEL.IsUnknown() {
		return diags
	}
	hasScope := scope != nil
	hasMatch := !matchCEL.IsNull()
	if hasScope == hasMatch {
		diags.AddError("Invalid budget scope",
			"Exactly one of `scope` or `match_cel` must be set.")
	}
	return diags
}

func (r *budgetResource) writeInput(ctx context.Context, m *budgetResourceModel) (client.BudgetWriteInput, error) {
	in := client.BudgetWriteInput{
		Period:     m.Limits.Period.ValueString(),
		Amount:     float64Ptr(m.Limits.Amount),
		TokenLimit: float64Ptr(m.Limits.TokenLimit),
		IsActive:   boolPtr(m.IsActive),
		ExpiresAt:  m.ExpiresAt.ValueString(),
	}
	if m.Scope != nil {
		in.ScopeKind = m.Scope.Kind.ValueString()
		in.ScopeTarget = m.Scope.Target.ValueString()
	} else if p := strPtr(m.MatchCEL); p != nil {
		in.MatchCEL = p
	}
	if p := int64Ptr(m.RateLimitPerMin); p != nil {
		v := int32(*p)
		in.RateLimit = &v
		in.SetRateLimit = true
	}
	for _, a := range m.Alerts {
		ids, diags := stringSlice(ctx, a.NotifierIDs)
		if diags.HasError() {
			return in, fmt.Errorf("reading notifier_ids")
		}
		dim := ""
		if !a.Dimension.IsNull() && !a.Dimension.IsUnknown() {
			dim = a.Dimension.ValueString()
		}
		// Carry the existing alert id (when known) so the server edits the alert
		// in place instead of deleting + recreating it, which would re-mint the
		// id on every update. Empty id => a new alert the server assigns.
		id := ""
		if !a.ID.IsNull() && !a.ID.IsUnknown() {
			id = a.ID.ValueString()
		}
		in.Alerts = append(in.Alerts, client.BudgetAlert{
			ID:               id,
			ThresholdPercent: int32(a.ThresholdPercent.ValueInt64()),
			NotifierIDs:      ids,
			Dimension:        dim,
		})
	}
	return in, nil
}

func (r *budgetResource) apply(b *client.Budget, m *budgetResourceModel) {
	m.ID = types.StringValue(b.ID)
	if b.ScopeKind != "" {
		m.Scope = &budgetScopeModel{
			Kind:   types.StringValue(b.ScopeKind),
			Target: optString(b.ScopeTarget),
		}
		m.MatchCEL = types.StringNull()
	} else {
		m.Scope = nil
		m.MatchCEL = optString(b.MatchCEL)
	}
	m.Limits = &budgetLimitsModel{
		// period is Required, so the server always echoes one; a null here would
		// contradict the config and abort the apply.
		Period:     types.StringValue(b.Period),
		Amount:     float64OrNull(b.Amount),
		TokenLimit: float64OrNull(b.TokenLimit),
	}
	if b.RateLimit != nil {
		m.RateLimitPerMin = types.Int64Value(int64(*b.RateLimit))
	} else {
		m.RateLimitPerMin = types.Int64Null()
	}
	m.IsActive = types.BoolValue(b.IsActive)
	// Store the server's normalized (UTC) value; the rfc3339Instant custom type
	// compares by instant, so this converges with a non-UTC config value.
	m.ExpiresAt = rfc3339InstantValue(b.ExpiresAt)
	m.CreatedAt = types.StringValue(b.CreatedAt)
	m.UpdatedAt = types.StringValue(b.UpdatedAt)

	if len(b.Alerts) == 0 {
		m.Alerts = nil
		return
	}
	alerts := make([]budgetAlertModel, 0, len(b.Alerts))
	for _, a := range b.Alerts {
		alerts = append(alerts, budgetAlertModel{
			ID:               types.StringValue(a.ID),
			ThresholdPercent: types.Int64Value(int64(a.ThresholdPercent)),
			NotifierIDs:      stringListValue(a.NotifierIDs),
			Dimension:        types.StringValue(a.Dimension),
		})
	}
	m.Alerts = alerts
}

func float64OrNull(p *float64) types.Float64 {
	if p == nil {
		return types.Float64Null()
	}
	return types.Float64Value(*p)
}

// ModifyPlan re-correlates each planned alert's server-issued id to the
// prior-state alert that shares its stable identity (threshold_percent +
// dimension) so a reordered / inserted / removed alert block keeps its OWN id.
// Terraform core merges nested list blocks positionally (prior[i] into plan[i]);
// for a swapped or shifted list that hands one alert's id to a DIFFERENT threshold
// — silently changing alert identity, which the server accepts (any id belonging
// to the budget is valid) and tracks fired alerts by. Only the computed `id` is
// touched (its config is always null), so AssertPlanValid never sees a non-null
// config value change.
func (r *budgetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroy (null plan) or create (null prior state): nothing to correlate.
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state, config budgetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(plan.Alerts) == 0 {
		return
	}
	correlateAlertIDs(plan.Alerts, config.Alerts, state.Alerts)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// correlateAlertIDs carries each matched alert's OWN prior server-issued id into
// the plan, undoing Terraform core's positional (by-index) merge of the computed
// `id`. It mutates plan in place.
//
// The identity key is (dimension, threshold_percent) — the tuple the server treats
// as an alert's identity (its duplicate fingerprint). Duplicate keys pair
// positionally among themselves (the first unused prior with that key pairs with
// the first plan element with that key, and so on): a stable tie-break the server
// does not forbid.
//
// config[i] (NOT plan[i]) supplies the key: plan[i]'s dimension may already be a
// wrong, positionally-merged value, whereas config carries the operator's own. A
// plan alert whose key is not yet known (an interpolated threshold/dimension), or
// that matches no prior alert, gets an unknown id ("known after apply"); unmatched
// prior ids are simply never carried over (the server deletes those alerts).
func correlateAlertIDs(plan, config, prior []budgetAlertModel) {
	stateByKey := make(map[string][]int, len(prior))
	for i, s := range prior {
		if k, ok := alertKey(s); ok {
			stateByKey[k] = append(stateByKey[k], i)
		}
	}
	used := make([]bool, len(prior))
	for i := range plan {
		var cfg budgetAlertModel
		if i < len(config) {
			cfg = config[i]
		}
		k, ok := alertKey(cfg)
		if !ok {
			// threshold or dimension not known yet — never guess.
			plan[i].ID = types.StringUnknown()
			continue
		}
		matched := -1
		for _, idx := range stateByKey[k] {
			if !used[idx] {
				matched = idx
				break
			}
		}
		if matched < 0 {
			plan[i].ID = types.StringUnknown() // new alert: id known only after apply.
			continue
		}
		used[matched] = true
		plan[i].ID = prior[matched].ID
	}
}

// alertKey is an alert's stable identity: its dimension (defaulting to the
// server's COST when unset) and threshold percent, formatted like the server's own
// duplicate fingerprint. ok is false when a value the key needs is not known at
// plan time, so the caller leaves the id for the server to assign rather than
// guessing a match.
func alertKey(a budgetAlertModel) (string, bool) {
	if a.ThresholdPercent.IsNull() || a.ThresholdPercent.IsUnknown() || a.Dimension.IsUnknown() {
		return "", false
	}
	dim := a.Dimension.ValueString() // "" when null / unset
	if dim == "" {
		dim = "COST" // the server's default dimension
	}
	return fmt.Sprintf("%s:%d", dim, a.ThresholdPercent.ValueInt64()), true
}

func (r *budgetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan budgetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Re-enforce the scope-XOR-match_cel invariant here: ValidateConfig defers it when
	// match_cel is unknown (interpolated), and the framework never re-runs
	// ValidateConfig at apply. match_cel is known now, so reject a forbidden
	// combination BEFORE the create call — otherwise writeInput silently picks scope
	// and discards match_cel (or vice versa) and the post-create state nulls the
	// dropped attribute.
	resp.Diagnostics.Append(validateBudgetScopeXOR(plan.Scope, plan.MatchCEL)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in, err := r.writeInput(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid budget input", err.Error())
		return
	}
	b, err := r.budgets.Create(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create budget", errDetail(err))
		return
	}
	r.apply(b, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *budgetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state budgetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.budgets.Get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read budget", errDetail(err))
		return
	}
	r.apply(b, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *budgetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan budgetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Re-enforce the scope-XOR-match_cel invariant (ValidateConfig defers it on an
	// unknown match_cel and is not re-run at apply — see validateBudgetScopeXOR).
	resp.Diagnostics.Append(validateBudgetScopeXOR(plan.Scope, plan.MatchCEL)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Alert ids were already matched to the prior state by stable key in
	// ModifyPlan, so plan.Alerts carries the right id for each alert (or an unknown
	// id for a new one); writeInput sends them as-is.
	in, err := r.writeInput(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid budget input", err.Error())
		return
	}
	// Update never sends the (immutable) scope; the rate limit and expiration
	// are reconciled to the config (cleared when absent).
	in.ScopeKind = ""
	in.ScopeTarget = ""
	in.SetRateLimit = true // reconcile: nil RateLimit clears it
	if plan.ExpiresAt.IsNull() {
		in.ClearExpiresAt = true
	}
	if len(plan.Alerts) == 0 {
		in.ClearAlerts = true
	}

	b, err := r.budgets.Update(ctx, plan.ID.ValueString(), in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update budget", errDetail(err))
		return
	}
	r.apply(b, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *budgetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state budgetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.budgets.Delete(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete budget", errDetail(err))
	}
}

func (r *budgetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
