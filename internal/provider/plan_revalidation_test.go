package provider

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// planFor builds a plan for a resource from a partially filled model. Every
// field left at its zero value is null, which is what an omitted attribute is.
func planFor(t *testing.T, r resource.Resource, model any) tfsdk.Plan {
	t.Helper()
	ctx := context.Background()
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	s := sch.Schema
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := state.Set(ctx, model); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}
	return tfsdk.Plan{Schema: s, Raw: state.Raw}
}

// markComputedNilsAsUnknown mirrors what the framework does to a plan before it
// reaches Create: every Computed attribute the config left null becomes
// "known after apply". A plan built without this is not a plan the resource
// would ever be handed.
func markComputedNilsAsUnknown(t *testing.T, s schema.Schema, raw tftypes.Value) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	out, err := tftypes.Transform(raw, func(p *tftypes.AttributePath, v tftypes.Value) (tftypes.Value, error) {
		if len(p.Steps()) == 0 || !v.IsNull() {
			return v, nil
		}
		a, err := s.AttributeAtTerraformPath(ctx, p)
		if err != nil {
			return v, nil // not an attribute path (an object inside a list, say)
		}
		if !a.IsComputed() {
			return v, nil
		}
		return tftypes.NewValue(v.Type(), tftypes.UnknownValue), nil
	})
	if err != nil {
		t.Fatalf("marking computed nils: %v", err)
	}
	return out
}

// planAfterMarking is planFor plus the framework's computed-nil marking — the
// shape a Create actually receives.
func planAfterMarking(t *testing.T, r resource.Resource, model any) tfsdk.Plan {
	t.Helper()
	plan := planFor(t, r, model)
	s := plan.Schema.(schema.Schema)
	return tfsdk.Plan{Schema: plan.Schema, Raw: markComputedNilsAsUnknown(t, s, plan.Raw)}
}

// syntheticValue builds a fully populated value of a type: every object holds
// every attribute, every collection holds one element, every primitive is
// concrete. The walk is value-driven — it descends into a nested object only
// when that object is present, and into list elements only when there are any —
// so a census run against a null plan would never reach a nested kind at all.
func syntheticValue(typ tftypes.Type) tftypes.Value {
	switch {
	case typ.Is(tftypes.String):
		return tftypes.NewValue(typ, "x")
	case typ.Is(tftypes.Bool):
		return tftypes.NewValue(typ, true)
	case typ.Is(tftypes.Number):
		return tftypes.NewValue(typ, big.NewFloat(1))
	}
	switch t := typ.(type) {
	case tftypes.Object:
		attrs := make(map[string]tftypes.Value, len(t.AttributeTypes))
		for name, at := range t.AttributeTypes {
			attrs[name] = syntheticValue(at)
		}
		return tftypes.NewValue(t, attrs)
	case tftypes.List:
		return tftypes.NewValue(t, []tftypes.Value{syntheticValue(t.ElementType)})
	case tftypes.Set:
		return tftypes.NewValue(t, []tftypes.Value{syntheticValue(t.ElementType)})
	case tftypes.Map:
		return tftypes.NewValue(t, map[string]tftypes.Value{"k": syntheticValue(t.ElementType)})
	case tftypes.Tuple:
		elems := make([]tftypes.Value, 0, len(t.ElementTypes))
		for _, et := range t.ElementTypes {
			elems = append(elems, syntheticValue(et))
		}
		return tftypes.NewValue(t, elems)
	}
	return tftypes.NewValue(typ, nil)
}

// census runs the real walker over a fully populated value, so every nested
// object and list element is visited and every attribute kind reaches the
// walker's dispatch. Only the unhandled list is of interest; the diagnostics the
// synthetic values provoke are meaningless and ignored.
func census(t *testing.T, s schema.Schema) []string {
	t.Helper()
	ctx := context.Background()
	w := &planWalker{cfg: tfsdk.Config{Schema: s, Raw: syntheticValue(s.Type().TerraformType(ctx))}}
	w.attributes(ctx, path.Empty(), s.Attributes)
	w.blocks(ctx, path.Empty(), s.Blocks)
	return w.unhandled
}

// TestRevalidatePlanWalksEverySchemaKind fails the build when a resource grows
// an attribute or block kind the walker cannot descend into — silently losing
// apply-time coverage for everything nested under it.
func TestRevalidatePlanWalksEverySchemaKind(t *testing.T) {
	ctx := context.Background()
	for _, f := range New("test")().Resources(ctx) {
		r := f()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "orq"}, &meta)
		var sch resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sch)

		if unhandled := census(t, sch.Schema); len(unhandled) > 0 {
			t.Errorf("%s: revalidatePlan cannot walk %v", meta.TypeName, unhandled)
		}
	}
}

// TestPlanWalkerReportsUnsupportedNestedKind is the census's own guard: an
// unsupported kind buried two levels down must be reported, not passed over.
// Without it a green census would prove only that the walk never got there.
func TestPlanWalkerReportsUnsupportedNestedKind(t *testing.T) {
	nested := schema.Schema{
		Attributes: map[string]schema.Attribute{
			"outer": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"inner": schema.ListNestedAttribute{
						Optional: true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								// The walker has no Set case, so this must surface.
								"tags": schema.SetAttribute{Optional: true, ElementType: types.StringType},
								"name": schema.StringAttribute{Optional: true},
							},
						},
					},
				},
			},
		},
	}
	unhandled := census(t, nested)
	if len(unhandled) != 1 || !strings.Contains(unhandled[0], "outer.inner[0].tags") {
		t.Fatalf("the census must report the nested Set attribute, got %v", unhandled)
	}
}

// rejectingStringValidator reports whatever value it is handed, so a test can
// tell "the validator ran" from "the validator was skipped".
type rejectingStringValidator struct{}

func (rejectingStringValidator) Description(context.Context) string { return "always reports" }
func (v rejectingStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (rejectingStringValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	resp.Diagnostics.AddAttributeError(req.Path, "Saw the value", req.ConfigValue.ValueString())
}

// unreadableStringType stands in for any custom type the walk cannot turn into a
// value — the case that must be RECORDED rather than passed over.
type unreadableStringType struct{ basetypes.StringType }

func (unreadableStringType) ValueFromTerraform(context.Context, tftypes.Value) (attr.Value, error) {
	return nil, errors.New("this type cannot be read")
}

// TestPlanWalkerValidatesCustomTypedAttributes covers the pass's blind spot:
// reading every attribute into the BASE type it wraps fails for a custom-typed
// one — rfc3339Instant on expires_at, jsontypes.Normalized on guardrail options
// — so its validators were skipped, and skipped silently. Reading through the
// attribute's own type reaches them.
//
// No validator lives on those attributes today, which is why this uses a
// test-only schema: it pins the mechanism without inventing a rule (an
// expires-in-the-future check would put a clock in the suite).
func TestPlanWalkerValidatesCustomTypedAttributes(t *testing.T) {
	ctx := context.Background()
	s := schema.Schema{
		Attributes: map[string]schema.Attribute{
			"expires_at": schema.StringAttribute{
				CustomType: rfc3339InstantType{},
				Optional:   true,
				Validators: []validator.String{rejectingStringValidator{}},
			},
		},
	}
	raw := tftypes.NewValue(s.Type().TerraformType(ctx), map[string]tftypes.Value{
		"expires_at": tftypes.NewValue(tftypes.String, "2030-01-01T00:00:00Z"),
	})

	diags := revalidatePlan(ctx, tfsdk.Plan{Schema: s, Raw: raw})
	if !diags.HasError() {
		t.Fatal("a validator on a custom-typed attribute must run")
	}
	if got := diags.Errors()[0].Detail(); got != "2030-01-01T00:00:00Z" {
		t.Errorf("the validator must receive the planned value, got %q", got)
	}
}

// A value the walk cannot read must land in unhandled, so the census fails
// rather than quietly dropping the attribute from the pass.
func TestPlanWalkerRecordsUnreadableAttribute(t *testing.T) {
	ctx := context.Background()
	s := schema.Schema{
		Attributes: map[string]schema.Attribute{
			"opaque": schema.StringAttribute{
				CustomType: unreadableStringType{},
				Optional:   true,
				Validators: []validator.String{rejectingStringValidator{}},
			},
		},
	}
	w := &planWalker{cfg: tfsdk.Config{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), map[string]tftypes.Value{
		"opaque": tftypes.NewValue(tftypes.String, "x"),
	})}}
	w.attributes(ctx, path.Empty(), s.Attributes)
	if len(w.unhandled) != 1 || !strings.Contains(w.unhandled[0], "opaque: unreadable") {
		t.Fatalf("an unreadable value must be recorded, got %v", w.unhandled)
	}
}

// TestRevalidatePlanRejectsResolvedValues drives the values a schema validator
// SKIPS at plan time because they were unknown then. The framework never re-runs
// those validators at apply, so each of these used to reach the server, come
// back normalized (uppercased enum, "" stored as absence) and fail the apply
// with an inconsistent result on an already-created resource.
// orq_project is deliberately absent: it declares no attribute validators at
// all, so there is no resolved value for this pass to reject. Its accept case
// still proves the walk runs over it.
func TestRevalidatePlanRejectsResolvedValues(t *testing.T) {
	ctx := context.Background()
	limits := &budgetLimitsModel{Period: types.StringValue("MONTHLY"), Amount: types.Float64Value(10)}

	cases := []struct {
		name     string
		resource resource.Resource
		model    any
		wantPath string
	}{
		{
			name: "budget scope kind resolved lowercase", resource: NewBudgetResource(),
			model:    &budgetResourceModel{Scope: &budgetScopeModel{Kind: types.StringValue("workspace")}, Limits: limits},
			wantPath: "scope.kind",
		},
		{
			name: "budget period resolved lowercase", resource: NewBudgetResource(),
			model: &budgetResourceModel{
				Scope:  &budgetScopeModel{Kind: types.StringValue(client.BudgetScopeWorkspace)},
				Limits: &budgetLimitsModel{Period: types.StringValue("monthly"), Amount: types.Float64Value(10)},
			},
			wantPath: "limits.period",
		},
		{
			name: "budget alert dimension resolved lowercase", resource: NewBudgetResource(),
			model: &budgetResourceModel{
				Scope:  &budgetScopeModel{Kind: types.StringValue(client.BudgetScopeWorkspace)},
				Limits: limits,
				Alerts: []budgetAlertModel{{
					ThresholdPercent: types.Int64Value(80),
					NotifierIDs:      stringList("nf_1"),
					Dimension:        types.StringValue("cost"),
				}},
			},
			wantPath: "alerts[0].dimension",
		},
		{
			name: "budget match_cel resolved empty", resource: NewBudgetResource(),
			model:    &budgetResourceModel{MatchCEL: types.StringValue(""), Limits: limits},
			wantPath: "match_cel",
		},
		{
			name: "budget scope target resolved empty", resource: NewBudgetResource(),
			model: &budgetResourceModel{
				Scope:  &budgetScopeModel{Kind: types.StringValue(client.BudgetScopeWorkspace), Target: types.StringValue("")},
				Limits: limits,
			},
			wantPath: "scope.target",
		},
		{
			name: "notifier type resolved lowercase", resource: NewNotifierResource(),
			model: &notifierResourceModel{
				DisplayName: types.StringValue("alerts"),
				Type:        types.StringValue("email"),
				Emails:      types.ListNull(types.StringType),
			},
			wantPath: "type",
		},
		{
			name: "notifier project_id resolved empty", resource: NewNotifierResource(),
			model: &notifierResourceModel{
				DisplayName: types.StringValue("alerts"),
				Type:        types.StringValue(client.NotifierTypeEmail),
				ProjectID:   types.StringValue(""),
				Emails:      types.ListNull(types.StringType),
			},
			wantPath: "project_id",
		},
		{
			name: "api_key project_id resolved empty", resource: NewAPIKeyResource(),
			model: &apiKeyResourceModel{
				Name:      types.StringValue("ci"),
				ProjectID: types.StringValue(""),
				Access:    types.MapNull(types.StringType),
			},
			wantPath: "project_id",
		},
		{
			name: "api_key access resolved empty", resource: NewAPIKeyResource(),
			model: &apiKeyResourceModel{
				Name:   types.StringValue("ci"),
				Access: types.MapValueMust(types.StringType, map[string]attr.Value{}),
			},
			wantPath: "access",
		},
		{
			name: "management_key access resolved empty", resource: NewManagementKeyResource(),
			model: &managementKeyResourceModel{
				Name:   types.StringValue("ci"),
				Access: types.MapValueMust(types.StringType, map[string]attr.Value{}),
			},
			wantPath: "access",
		},
		{
			name: "routing_rule project_id resolved empty", resource: NewRoutingRuleResource(),
			model:    &routingRuleResourceModel{DisplayName: types.StringValue("route"), ProjectID: types.StringValue("")},
			wantPath: "project_id",
		},
		{
			name: "routing_rule expression cel resolved empty", resource: NewRoutingRuleResource(),
			model: &routingRuleResourceModel{
				DisplayName: types.StringValue("route"),
				Expression:  &routingExpressionModel{Cel: types.StringValue("")},
			},
			wantPath: "expression.cel",
		},
		{
			name: "guardrail_rule project_id resolved empty", resource: NewGuardrailRuleResource(),
			model:    &guardrailRuleResourceModel{DisplayName: types.StringValue("pii"), ProjectID: types.StringValue("")},
			wantPath: "project_id",
		},
		{
			name: "bedrock_model model_family resolved empty", resource: NewBedrockModelResource(),
			model:    &bedrockModelResourceModel{DisplayName: types.StringValue("bedrock"), ModelFamily: types.StringValue("")},
			wantPath: "model_family",
		},
		{
			name: "bedrock_model assume_role_arn resolved empty", resource: NewBedrockModelResource(),
			model:    &bedrockModelResourceModel{DisplayName: types.StringValue("bedrock"), AssumeRoleArn: types.StringValue("")},
			wantPath: "assume_role_arn",
		},
		{
			name: "workspace_settings display_name resolved padded", resource: NewWorkspaceSettingsResource(),
			model:    &workspaceSettingsResourceModel{DisplayName: types.StringValue(" Acme ")},
			wantPath: "display_name",
		},
		{
			name: "management_key permission_mode resolved lowercase", resource: NewManagementKeyResource(),
			model: &managementKeyResourceModel{
				Name:           types.StringValue("ci"),
				PermissionMode: types.StringValue("management_permission_mode_all"),
				Access:         types.MapNull(types.StringType),
			},
			wantPath: "permission_mode",
		},
		{
			name: "guardrail_rule execute_on resolved capitalized", resource: NewGuardrailRuleResource(),
			model: &guardrailRuleResourceModel{
				DisplayName: types.StringValue("pii"),
				Guardrails: []guardrailRefModel{{
					ID:        types.StringValue("orq_pii_detection"),
					ExecuteOn: types.StringValue("Input"),
					Options:   jsontypes.NewNormalizedNull(),
				}},
			},
			wantPath: "guardrails[0].execute_on",
		},
		{
			name: "routing_rule models_config mode resolved capitalized", resource: NewRoutingRuleResource(),
			model: &routingRuleResourceModel{
				DisplayName: types.StringValue("route"),
				ModelsConfig: &routingRuleModelsConfigModel{
					Mode: types.StringValue("Weighted"),
					Models: []routingRuleModelRefModel{{
						Model:         types.StringValue("openai/gpt-4o"),
						DisplayName:   types.StringNull(),
						Weight:        types.Float64Null(),
						IntegrationID: types.StringNull(),
					}},
				},
			},
			wantPath: "models_config.mode",
		},
		{
			name: "model region resolved empty", resource: NewModelResource(),
			model: &modelResourceModel{
				DisplayName: types.StringValue("my model"),
				ModelID:     types.StringValue("liquid/lfm2.5-1.2b"),
				ModelType:   types.StringValue("chat"),
				Region:      types.StringValue(""),
				BaseURL:     types.StringValue("https://models.example.test/v1"),
				APIKey:      types.StringValue("sk-test"),
			},
			wantPath: "region",
		},
		{
			name: "bedrock_model auth_mode resolved unknown value", resource: NewBedrockModelResource(),
			model: &bedrockModelResourceModel{
				DisplayName: types.StringValue("bedrock"),
				AuthMode:    types.StringValue("none"),
			},
			wantPath: "auth_mode",
		},
		{
			name: "workspace_settings pii language resolved unsupported", resource: NewWorkspaceSettingsResource(),
			model: &workspaceSettingsResourceModel{
				PiiRedaction: &workspaceSettingsPiiModel{
					Enabled: types.BoolValue(true),
					Config: &workspaceSettingsPiiConfigModel{
						Language:  types.StringValue("fr"),
						Entities:  types.ListNull(types.StringType),
						OnFailure: types.StringNull(),
						Threshold: types.Float64Null(),
					},
				},
			},
			wantPath: "pii_redaction.config.language",
		},
		{
			name: "evaluator type resolved uppercase", resource: NewEvaluatorResource(),
			model: &evaluatorResourceModel{
				Key:       types.StringValue("my-eval"),
				Type:      types.StringValue("LLM_EVAL"),
				ProjectID: types.StringValue("01JMDPA3QW5C1V0NJ1PW34T4E5"),
			},
			wantPath: "type",
		},
		{
			// Deep inside two nested lists and an object — the walk has to descend
			// the whole way for the retry budget to be re-checked at all.
			name: "evaluator judge retry count resolved out of range", resource: NewEvaluatorResource(),
			model: &evaluatorResourceModel{
				Key:       types.StringValue("jury-eval"),
				Type:      types.StringValue(client.EvaluatorTypeLLM),
				ProjectID: types.StringValue("01JMDPA3QW5C1V0NJ1PW34T4E5"),
				Mode:      types.StringValue("jury"),
				Jury: &evaluatorJuryModel{
					Judges: []evaluatorJudgeModel{
						{Model: types.StringValue("openai/gpt-4o")},
						{
							Model: types.StringValue("anthropic/claude-sonnet-4"),
							Retry: &evaluatorRetryModel{Count: types.Int64Value(9)},
						},
					},
					MinSuccessfulJudges: types.Int64Value(2),
				},
			},
			wantPath: "jury.judges[1].retry.count",
		},
		{
			name: "workspace_model all_projects resolved false", resource: NewWorkspaceModelResource(),
			model: &workspaceModelResourceModel{
				ModelID: types.StringValue("openai/gpt-4o"),
				Sharing: &workspaceModelSharingModel{AllProjects: types.BoolValue(false), ProjectIDs: types.ListNull(types.StringType)},
			},
			wantPath: "sharing.all_projects",
		},
		{
			name: "workspace_model duplicate project_ids", resource: NewWorkspaceModelResource(),
			model: &workspaceModelResourceModel{
				ModelID: types.StringValue("openai/gpt-4o"),
				Sharing: &workspaceModelSharingModel{AllProjects: types.BoolNull(), ProjectIDs: stringList("p1", "p1")},
			},
			wantPath: "sharing.project_ids[1]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := revalidatePlan(ctx, planFor(t, tc.resource, tc.model))
			if !diags.HasError() {
				t.Fatal("the resolved value must be rejected before the write")
			}
			var paths []string
			for _, e := range diags.Errors() {
				if a, ok := e.(interface{ Path() path.Path }); ok {
					paths = append(paths, a.Path().String())
				}
			}
			if !slicesContains(paths, tc.wantPath) {
				t.Errorf("expected an error at %q, got %v (%v)", tc.wantPath, paths, diags)
			}
		})
	}
}

func slicesContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// The create variant above is only meaningful if the marking really happens:
// a helper that silently no-opped would test the update shape twice.
func TestMarkComputedNilsAsUnknown(t *testing.T) {
	ctx := context.Background()
	plan := planAfterMarking(t, NewAPIKeyResource(), &apiKeyResourceModel{
		Name:   types.StringValue("ci"),
		Access: types.MapNull(types.StringType),
	})
	var m apiKeyResourceModel
	if diags := plan.Get(ctx, &m); diags.HasError() {
		t.Fatalf("reading plan: %v", diags)
	}
	if !m.ID.IsUnknown() || !m.TokenPrefix.IsUnknown() {
		t.Errorf("Computed nils must become known-after-apply, got id=%v token_prefix=%v", m.ID, m.TokenPrefix)
	}
	if !m.PermissionMode.IsUnknown() {
		t.Errorf("an Optional+Computed nil must become known-after-apply, got %v", m.PermissionMode)
	}
	if m.Name.ValueString() != "ci" || !m.Access.IsNull() {
		t.Errorf("a written value and a non-Computed nil must be left alone: name=%v access=%v", m.Name, m.Access)
	}
}

// TestRevalidatePlanAcceptsValidPlans is the other half of the contract: the
// pass re-runs the validators the plan phase already ran, so a plan the operator
// could legitimately produce must survive it. Every registered resource is
// covered, in both shapes it is handed at apply — a create, where the framework
// has marked every Computed nil "known after apply", and an update, where those
// attributes carry the values a previous apply left in state.
func TestRevalidatePlanAcceptsValidPlans(t *testing.T) {
	ctx := context.Background()
	const ulid = "01JMDPA3QW5C1V0NJ1PW34T4E5"

	judge := func(model string) evaluatorJudgeModel {
		return evaluatorJudgeModel{
			Model:     types.StringValue(model),
			Retry:     &evaluatorRetryModel{Count: types.Int64Value(3), OnCodes: []types.Int64{types.Int64Value(429), types.Int64Value(500)}},
			Fallbacks: []types.String{types.StringValue("openai/gpt-4o-mini")},
		}
	}
	budgetLimits := func() *budgetLimitsModel {
		return &budgetLimitsModel{Period: types.StringValue("MONTHLY"), Amount: types.Float64Value(10)}
	}
	routingModels := func(weight types.Float64, name, integration types.String) []routingRuleModelRefModel {
		return []routingRuleModelRefModel{{
			Model:         types.StringValue("openai/gpt-4o"),
			DisplayName:   name,
			Weight:        weight,
			IntegrationID: integration,
		}}
	}
	accessMap := stringMapValue(map[string]string{"project": client.AccessLevelWrite})

	// config is what the operator wrote; prior, when given, is the same resource
	// after an apply — the Optional+Computed attributes now holding server values.
	cases := []struct {
		name     string
		resource resource.Resource
		config   any
		prior    any
	}{
		{
			name: "project", resource: NewProjectResource(),
			config: &projectResourceModel{
				Name:        types.StringValue("Acme"),
				Description: types.StringValue("the acme workspace"),
				Teams:       stringList("team_1"),
			},
		},
		{
			name: "budget workspace scope", resource: NewBudgetResource(),
			config: &budgetResourceModel{
				Scope:  &budgetScopeModel{Kind: types.StringValue(client.BudgetScopeWorkspace)},
				Limits: budgetLimits(),
			},
		},
		{
			name: "budget match_cel with alerts", resource: NewBudgetResource(),
			config: &budgetResourceModel{
				MatchCEL:        types.StringValue(`provider == "openai"`),
				Limits:          budgetLimits(),
				RateLimitPerMin: types.Int64Value(60),
				Alerts: []budgetAlertModel{{
					ThresholdPercent: types.Int64Value(80),
					NotifierIDs:      stringList("nf_1"),
				}},
			},
			prior: &budgetResourceModel{
				MatchCEL:        types.StringValue(`provider == "openai"`),
				Limits:          budgetLimits(),
				RateLimitPerMin: types.Int64Value(60),
				IsActive:        types.BoolValue(true),
				Alerts: []budgetAlertModel{{
					ID:               types.StringValue("bal_1"),
					ThresholdPercent: types.Int64Value(80),
					NotifierIDs:      stringList("nf_1"),
					Dimension:        types.StringValue("COST"),
				}},
			},
		},
		{
			name: "notifier email", resource: NewNotifierResource(),
			config: &notifierResourceModel{
				DisplayName: types.StringValue("alerts"),
				Type:        types.StringValue(client.NotifierTypeEmail),
				Emails:      stringList("ops@x.io"),
			},
			prior: &notifierResourceModel{
				DisplayName: types.StringValue("alerts"),
				Type:        types.StringValue(client.NotifierTypeEmail),
				Emails:      stringList("ops@x.io"),
				ProjectID:   types.StringValue("proj_1"),
			},
		},
		{
			name: "notifier webhook with no recipients", resource: NewNotifierResource(),
			config: &notifierResourceModel{
				DisplayName: types.StringValue("hook"),
				Type:        types.StringValue(client.NotifierTypeWebhook),
				Emails:      stringList(),
				WebhookURL:  types.StringValue("https://example.test/hook"),
			},
		},
		{
			name: "guardrail_rule built-in and custom evaluator", resource: NewGuardrailRuleResource(),
			config: &guardrailRuleResourceModel{
				DisplayName: types.StringValue("pii"),
				ProjectID:   types.StringValue(ulid),
				Guardrails: []guardrailRefModel{
					{
						ID:         types.StringValue("orq_pii_detection"),
						ExecuteOn:  types.StringValue("input"),
						Options:    jsontypes.NewNormalizedValue(`{"threshold":0.5}`),
						SampleRate: types.Float64Value(1),
					},
					{
						ID:          types.StringValue(ulid),
						ExecuteOn:   types.StringValue("output"),
						Options:     jsontypes.NewNormalizedNull(),
						IsGuardrail: types.BoolValue(true),
					},
				},
			},
		},
		{
			name: "workspace_model all projects", resource: NewWorkspaceModelResource(),
			config: &workspaceModelResourceModel{
				ModelID: types.StringValue("openai/gpt-4o"),
				Sharing: &workspaceModelSharingModel{AllProjects: types.BoolValue(true), ProjectIDs: types.ListNull(types.StringType)},
			},
			prior: &workspaceModelResourceModel{
				ModelID: types.StringValue("openai/gpt-4o"),
				Sharing: &workspaceModelSharingModel{
					AllProjects:          types.BoolValue(true),
					ProjectIDs:           types.ListNull(types.StringType),
					AllowVersionPin:      types.BoolValue(false),
					AllowFork:            types.BoolValue(false),
					AutoGrantNewProjects: types.BoolValue(true),
				},
			},
		},
		{
			name: "workspace_model selected projects", resource: NewWorkspaceModelResource(),
			config: &workspaceModelResourceModel{
				ModelID: types.StringValue("openai/gpt-4o"),
				Sharing: &workspaceModelSharingModel{AllProjects: types.BoolNull(), ProjectIDs: stringList("p2", "p1")},
			},
		},
		{
			name: "routing_rule with expression and models_config", resource: NewRoutingRuleResource(),
			config: &routingRuleResourceModel{
				DisplayName: types.StringValue("route"),
				ProjectID:   types.StringValue(ulid),
				Expression:  &routingExpressionModel{Cel: types.StringValue(`model == "gpt-4"`)},
				ModelsConfig: &routingRuleModelsConfigModel{
					Mode:   types.StringValue("weighted"),
					Models: routingModels(types.Float64Null(), types.StringNull(), types.StringNull()),
				},
			},
			prior: &routingRuleResourceModel{
				DisplayName: types.StringValue("route"),
				ProjectID:   types.StringValue(ulid),
				Description: types.StringValue("from the server"),
				Enabled:     types.BoolValue(true),
				Priority:    types.Int64Value(0),
				Expression:  &routingExpressionModel{Cel: types.StringValue(`model == "gpt-4"`)},
				ModelsConfig: &routingRuleModelsConfigModel{
					Mode:   types.StringValue("weighted"),
					Models: routingModels(types.Float64Value(0.5), types.StringValue(""), types.StringValue("")),
				},
			},
		},
		{
			name: "routing_rule match only", resource: NewRoutingRuleResource(),
			config: &routingRuleResourceModel{
				DisplayName: types.StringValue("match-only"),
			},
		},
		{
			name: "api_key all projects", resource: NewAPIKeyResource(),
			config: &apiKeyResourceModel{
				Name:           types.StringValue("ci"),
				PermissionMode: types.StringValue(client.PermissionModeAll),
				Access:         types.MapNull(types.StringType),
			},
		},
		{
			name: "api_key restricted", resource: NewAPIKeyResource(),
			config: &apiKeyResourceModel{
				Name:           types.StringValue("ci"),
				ProjectID:      types.StringValue(ulid),
				PermissionMode: types.StringValue(client.PermissionModeRestricted),
				Access:         accessMap,
			},
		},
		{
			name: "management_key all", resource: NewManagementKeyResource(),
			config: &managementKeyResourceModel{
				Name:           types.StringValue("ci"),
				PermissionMode: types.StringValue(client.ManagementPermissionModeAll),
				Access:         types.MapNull(types.StringType),
			},
		},
		{
			name: "management_key restricted", resource: NewManagementKeyResource(),
			config: &managementKeyResourceModel{
				Name:           types.StringValue("ci"),
				PermissionMode: types.StringValue(client.ManagementPermissionModeRestricted),
				Access:         accessMap,
			},
		},
		{
			name: "model custom openai-like", resource: NewModelResource(),
			config: &modelResourceModel{
				DisplayName: types.StringValue("my model"),
				ModelID:     types.StringValue("liquid/lfm2.5-1.2b"),
				ModelType:   types.StringValue("chat"),
				Region:      types.StringValue("europe"),
				BaseURL:     types.StringValue("https://models.example.test/v1"),
				APIKey:      types.StringValue("sk-test"),
			},
			prior: &modelResourceModel{
				DisplayName: types.StringValue("my model"),
				ModelID:     types.StringValue("liquid/lfm2.5-1.2b"),
				ModelType:   types.StringValue("chat"),
				Region:      types.StringValue("europe"),
				BaseURL:     types.StringValue("https://models.example.test/v1"),
				APIKey:      types.StringValue("sk-test"),
				Description: types.StringValue(""),
				InputCost:   types.Float64Value(0),
				OutputCost:  types.Float64Value(0),
				MaxTokens:   types.Int64Value(4096),
				Temperature: types.Float64Value(0.7),
			},
		},
		{
			name: "bedrock_model pod identity", resource: NewBedrockModelResource(),
			config: &bedrockModelResourceModel{
				DisplayName:          types.StringValue("bedrock"),
				ModelID:              types.StringValue("arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123"),
				Region:               types.StringValue("eu-central-1"),
				ModelDeveloper:       types.StringValue("anthropic"),
				AuthMode:             types.StringValue(client.BedrockAuthModePodIdentity),
				AssumeRoleArn:        types.StringValue("arn:aws:iam::123456789012:role/bedrock"),
				AssumeRoleExternalID: types.StringValue("ext-1"),
			},
			prior: &bedrockModelResourceModel{
				DisplayName:          types.StringValue("bedrock"),
				ModelID:              types.StringValue("arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123"),
				Region:               types.StringValue("eu-central-1"),
				ModelDeveloper:       types.StringValue("anthropic"),
				AuthMode:             types.StringValue(client.BedrockAuthModePodIdentity),
				AssumeRoleArn:        types.StringValue("arn:aws:iam::123456789012:role/bedrock"),
				AssumeRoleExternalID: types.StringValue("ext-1"),
				ModelType:            types.StringValue("chat"),
				ModelFamily:          types.StringValue("claude"),
				Description:          types.StringValue(""),
			},
		},
		{
			name: "bedrock_model integration", resource: NewBedrockModelResource(),
			config: &bedrockModelResourceModel{
				DisplayName:    types.StringValue("bedrock"),
				ModelID:        types.StringValue("arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123"),
				Region:         types.StringValue("eu-central-1"),
				ModelDeveloper: types.StringValue("anthropic"),
				AuthMode:       types.StringValue(client.BedrockAuthModeIntegration),
				IntegrationID:  types.StringValue(ulid),
				MaxTokens:      types.Int64Value(4096),
				Temperature:    types.Float64Value(0.2),
			},
		},
		{
			name: "workspace_settings with pii redaction", resource: NewWorkspaceSettingsResource(),
			config: &workspaceSettingsResourceModel{
				DisplayName: types.StringValue("Acme"),
				PiiRedaction: &workspaceSettingsPiiModel{
					Enabled: types.BoolValue(true),
					Config: &workspaceSettingsPiiConfigModel{
						Language:  types.StringValue("en"),
						Entities:  stringList("EMAIL_ADDRESS", "PERSON"),
						OnFailure: types.StringValue("block"),
						Threshold: types.Float64Value(0.7),
					},
				},
			},
		},
		{
			name: "workspace_settings adopted", resource: NewWorkspaceSettingsResource(),
			config: &workspaceSettingsResourceModel{
				EnforceEnabledModels: types.BoolValue(true),
			},
			prior: &workspaceSettingsResourceModel{
				DisplayName:          types.StringValue("Acme"),
				EnforceEnabledModels: types.BoolValue(true),
			},
		},
		{
			name: "evaluator python", resource: NewEvaluatorResource(),
			config: &evaluatorResourceModel{
				Key:        types.StringValue("my-eval"),
				Type:       types.StringValue(client.EvaluatorTypePython),
				ProjectID:  types.StringValue(ulid),
				OutputType: types.StringValue("boolean"),
				Code:       types.StringValue("def evaluate(log): return True"),
			},
			prior: &evaluatorResourceModel{
				Key:         types.StringValue("my-eval"),
				Type:        types.StringValue(client.EvaluatorTypePython),
				ProjectID:   types.StringValue(ulid),
				OutputType:  types.StringValue("boolean"),
				Code:        types.StringValue("def evaluate(log): return True"),
				Description: types.StringValue(""),
				Repetitions: types.Int64Value(1),
			},
		},
		{
			name: "evaluator llm single with categorical labels", resource: NewEvaluatorResource(),
			config: &evaluatorResourceModel{
				Key:        types.StringValue("judge"),
				Type:       types.StringValue(client.EvaluatorTypeLLM),
				ProjectID:  types.StringValue(ulid),
				OutputType: types.StringValue("categorical"),
				Prompt:     types.StringValue("Is the answer helpful?"),
				Mode:       types.StringValue("single"),
				Model:      types.StringValue("openai/gpt-4o"),
				CategoricalLabels: []evaluatorLabelModel{
					{Value: types.StringValue("good"), Description: types.StringValue("helpful")},
					{Value: types.StringValue("bad"), Description: types.StringNull()},
				},
			},
		},
		{
			name: "evaluator llm jury", resource: NewEvaluatorResource(),
			config: &evaluatorResourceModel{
				Key:        types.StringValue("jury-eval"),
				Type:       types.StringValue(client.EvaluatorTypeLLM),
				ProjectID:  types.StringValue(ulid),
				OutputType: types.StringValue("boolean"),
				Prompt:     types.StringValue("Is the answer helpful?"),
				Mode:       types.StringValue("jury"),
				Jury: &evaluatorJuryModel{
					Judges:              []evaluatorJudgeModel{judge("openai/gpt-4o"), judge("anthropic/claude-sonnet-4")},
					ReplacementJudges:   []evaluatorJudgeModel{judge("openai/gpt-4o-mini")},
					MinSuccessfulJudges: types.Int64Value(2),
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+" create", func(t *testing.T) {
			if diags := revalidatePlan(ctx, planAfterMarking(t, tc.resource, tc.config)); diags.HasError() {
				t.Errorf("a valid create plan must pass: %v", diags)
			}
		})
		t.Run(tc.name+" update", func(t *testing.T) {
			model := tc.prior
			if model == nil {
				model = tc.config
			}
			if diags := revalidatePlan(ctx, planFor(t, tc.resource, model)); diags.HasError() {
				t.Errorf("a valid update plan must pass: %v", diags)
			}
		})
	}
}

// The pass is wired ahead of every write, not merely available.
func TestRevalidatePlanRunsBeforeTheWrite(t *testing.T) {
	ctx := context.Background()
	s := notifierSchema(t)
	plan := planFor(t, NewNotifierResource(), &notifierResourceModel{
		DisplayName: types.StringValue("alerts"),
		Type:        types.StringValue("email"), // an interpolation that resolved lowercase
		Emails:      stringList("ops@x.io"),
	})
	emptyState := func() tfsdk.State {
		return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	}

	api := &fakeNotifiers{}
	createResp := resource.CreateResponse{State: emptyState()}
	(&notifierResource{notifiers: api}).Create(ctx, resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("a lowercase type must fail the create")
	}
	updateResp := resource.UpdateResponse{State: emptyState()}
	(&notifierResource{notifiers: api}).Update(ctx, resource.UpdateRequest{Plan: plan, State: emptyState()}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Fatal("a lowercase type must fail the update")
	}
	if api.creates != 0 || api.updates != 0 {
		t.Errorf("nothing must be written: %d creates, %d updates", api.creates, api.updates)
	}
}
