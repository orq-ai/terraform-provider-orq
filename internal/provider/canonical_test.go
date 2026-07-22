package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// modelsConfigEqual reports whether two model-config strings are semantically
// equal under the custom type (the comparison Terraform uses for the attribute).
func modelsConfigEqual(t *testing.T, a, b string) bool {
	t.Helper()
	eq, diags := modelsConfigFromRaw([]byte(a)).StringSemanticEquals(context.Background(), modelsConfigFromRaw([]byte(b)))
	if diags.HasError() {
		t.Fatalf("models_config semantic-equality diags: %v", diags)
	}
	return eq
}

func retryConfigEqual(t *testing.T, a, b string) bool {
	t.Helper()
	eq, diags := retryConfigFromRaw([]byte(a)).StringSemanticEquals(context.Background(), retryConfigFromRaw([]byte(b)))
	if diags.HasError() {
		t.Fatalf("retry_config semantic-equality diags: %v", diags)
	}
	return eq
}

// TestModelsConfigSemanticEquals proves the weight-0.5 canonicalization holds in
// BOTH directions and tolerates formatting differences, while still catching a
// genuine value difference.
func TestModelsConfigSemanticEquals(t *testing.T) {
	weightless := `{"mode":"fallback","models":[{"model":"x"},{"model":"y","weight":0.25}]}`
	serverForm := `{"mode":"fallback","models":[{"model":"x","weight":0.5},{"model":"y","weight":0.25}]}`

	if !modelsConfigEqual(t, weightless, serverForm) {
		t.Error("weight-less config must equal the server's weight-0.5 read-back")
	}
	if !modelsConfigEqual(t, serverForm, weightless) {
		t.Error("semantic equality must be symmetric (server form vs weight-less)")
	}
	// Zero weight is coerced to 0.5 as well.
	if !modelsConfigEqual(t, `{"models":[{"model":"x","weight":0}]}`, `{"models":[{"model":"x","weight":0.5}]}`) {
		t.Error("zero weight must be treated as the server default 0.5")
	}
	// Formatting-only differences (key order, whitespace) are equal.
	if !modelsConfigEqual(t, `{"mode":"fallback","models":[{"model":"x","weight":0.5}]}`,
		`{ "models": [ {"weight":0.5,"model":"x"} ], "mode":"fallback" }`) {
		t.Error("key order / whitespace must not produce a diff")
	}
	// A genuine value difference is NOT equal.
	if modelsConfigEqual(t, `{"models":[{"model":"x","weight":0.5}]}`, `{"models":[{"model":"x","weight":0.75}]}`) {
		t.Error("differing explicit weights must not be equal")
	}
	if modelsConfigEqual(t, `{"models":[{"model":"x"}]}`, `{"models":[{"model":"z"}]}`) {
		t.Error("differing model names must not be equal")
	}
}

// TestModelsConfigWeightSpelling proves the weight is normalized to canonical
// float64 spelling so an operator's `0.50` / `1` / `5e-1` converges with the
// server's float64 read-back (`0.5` / `1`), while genuine value differences stay
// unequal.
func TestModelsConfigWeightSpelling(t *testing.T) {
	// Differing textual spellings of the same numeric weight are equal.
	for _, tc := range []struct{ a, b string }{
		{`{"models":[{"model":"x","weight":0.50}]}`, `{"models":[{"model":"x","weight":0.5}]}`},
		{`{"models":[{"model":"x","weight":0.500}]}`, `{"models":[{"model":"x","weight":0.5}]}`},
		{`{"models":[{"model":"x","weight":5e-1}]}`, `{"models":[{"model":"x","weight":0.5}]}`},
		{`{"models":[{"model":"x","weight":1}]}`, `{"models":[{"model":"x","weight":1.0}]}`},
		{`{"models":[{"model":"x","weight":1.00}]}`, `{"models":[{"model":"x","weight":1}]}`},
	} {
		if !modelsConfigEqual(t, tc.a, tc.b) {
			t.Errorf("%s must equal %s (same numeric weight, different spelling)", tc.a, tc.b)
		}
		if !modelsConfigEqual(t, tc.b, tc.a) {
			t.Errorf("weight-spelling equality must be symmetric: %s vs %s", tc.b, tc.a)
		}
	}
	// Genuinely different weights across a fallback config remain unequal.
	if modelsConfigEqual(t,
		`{"models":[{"model":"x","weight":0.3},{"model":"y","weight":0.7}]}`,
		`{"models":[{"model":"x","weight":0.7},{"model":"y","weight":0.3}]}`) {
		t.Error("0.3/0.7 must NOT equal 0.7/0.3 — spelling normalization must not collapse real value differences")
	}
	if modelsConfigEqual(t, `{"models":[{"model":"x","weight":0.3}]}`, `{"models":[{"model":"x","weight":0.7}]}`) {
		t.Error("0.3 must NOT equal 0.7")
	}
}

// TestRetryConfigOnCodesStayIntegers guards that number-spelling normalization is
// scoped to weight only: retry_config.on_codes are integer HTTP status codes and
// must NOT be float-normalized (429, never 429).
func TestRetryConfigOnCodesStayIntegers(t *testing.T) {
	got, ok := canonicalizeJSON(`{"count":3,"on_codes":[429,503]}`, retryConfigCanon)
	if !ok {
		t.Fatal("canonicalize failed")
	}
	if got != `{"count":3,"on_codes":[429,503]}` {
		t.Errorf("on_codes must stay integer-spelled, got %q", got)
	}
}

// TestRetryConfigSemanticEquals proves `on_codes: []` and a null/absent on_codes
// are all equal, while a populated on_codes is preserved.
func TestRetryConfigSemanticEquals(t *testing.T) {
	for _, empty := range []string{`{"count":3,"on_codes":[]}`, `{"count":3,"on_codes":null}`} {
		if !retryConfigEqual(t, empty, `{"count":3}`) {
			t.Errorf("%s must equal the server's elided {\"count\":3}", empty)
		}
		if !retryConfigEqual(t, `{"count":3}`, empty) {
			t.Errorf("semantic equality must be symmetric for %s", empty)
		}
	}
	// A populated on_codes is a real value: preserved and compared.
	if !retryConfigEqual(t, `{"count":3,"on_codes":[429,503]}`, `{"count":3,"on_codes":[429,503]}`) {
		t.Error("identical populated on_codes must be equal")
	}
	if retryConfigEqual(t, `{"count":3,"on_codes":[429]}`, `{"count":3}`) {
		t.Error("a populated on_codes must NOT equal an absent one")
	}
	if retryConfigEqual(t, `{"count":3,"on_codes":[429,503]}`, `{"count":3,"on_codes":[500]}`) {
		t.Error("differing on_codes must not be equal")
	}
}

// TestNullAndUnknownSemanticEquals proves null/unknown fall back to exact
// equality (semantic equality is never meant to bridge null vs non-null).
func TestNullAndUnknownSemanticEquals(t *testing.T) {
	nullV := modelsConfigFromRaw(nil)
	if !nullV.IsNull() {
		t.Fatal("modelsConfigFromRaw(nil) should be null")
	}
	// null vs null -> equal.
	eq, _ := nullV.StringSemanticEquals(context.Background(), modelsConfigFromRaw(nil))
	if !eq {
		t.Error("null vs null must be equal")
	}
	// null vs a value -> NOT equal (retain-on-null is handled by Computed, not here).
	eq, _ = nullV.StringSemanticEquals(context.Background(), modelsConfigFromRaw([]byte(`{"models":[]}`)))
	if eq {
		t.Error("null vs non-null must NOT be semantically equal")
	}
}

// TestCanonAttributesHaveNoPlanModifier is the regression guard for the redesign:
// the broken approach used a plan modifier that rewrote a non-null config value
// at plan time. These attributes must now rely purely on the custom type's
// semantic equality — NO plan modifier — or Terraform's AssertPlanValid rejects a
// from-scratch weight-less create.
func TestCanonAttributesHaveNoPlanModifier(t *testing.T) {
	ctx := context.Background()

	var routing resource.SchemaResponse
	NewRoutingRuleResource().Schema(ctx, resource.SchemaRequest{}, &routing)
	assertStringAttrNoPlanModifier(t, routing.Schema.Attributes, "models_config")
	if _, ok := routing.Schema.Attributes["models_config"].(schema.StringAttribute).CustomType.(modelsConfigType); !ok {
		t.Error("routing_rule.models_config must use modelsConfigType")
	}

	var policy resource.SchemaResponse
	NewPolicyResource().Schema(ctx, resource.SchemaRequest{}, &policy)
	assertStringAttrNoPlanModifier(t, policy.Schema.Attributes, "models_config")
	assertStringAttrNoPlanModifier(t, policy.Schema.Attributes, "retry_config")
	if _, ok := policy.Schema.Attributes["models_config"].(schema.StringAttribute).CustomType.(modelsConfigType); !ok {
		t.Error("policy.models_config must use modelsConfigType")
	}
	if _, ok := policy.Schema.Attributes["retry_config"].(schema.StringAttribute).CustomType.(retryConfigType); !ok {
		t.Error("policy.retry_config must use retryConfigType")
	}

	// The nested evaluator `options` attribute must likewise carry no plan modifier.
	block, ok := policy.Schema.Blocks["evaluators"].(schema.ListNestedBlock)
	if !ok {
		t.Fatal("policy evaluators is not a ListNestedBlock")
	}
	assertStringAttrNoPlanModifier(t, block.NestedObject.Attributes, "options")
}

func assertStringAttrNoPlanModifier(t *testing.T, attrs map[string]schema.Attribute, name string) {
	t.Helper()
	attr, ok := attrs[name].(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s is not a StringAttribute", name)
	}
	if len(attr.PlanModifiers) != 0 {
		t.Errorf("%s must have NO plan modifier (semantic equality only), found %d", name, len(attr.PlanModifiers))
	}
}

// TestPolicyModelsConfigApplyConverges proves the full round-trip: an operator's
// weight-less config and the value apply() stores from the server read-back
// (weight 0.5) are semantically equal — so post-apply reconciliation and the next
// plan see no diff.
func TestPolicyModelsConfigApplyConverges(t *testing.T) {
	config := `{"mode":"fallback","models":[{"model":"x"}]}`

	r := &policyResource{}
	var m policyResourceModel
	r.apply(&client.Policy{
		ID:           "pol_1",
		ModelsConfig: []byte(`{"mode":"fallback","models":[{"model":"x","weight":0.5}]}`),
	}, &m)

	if m.ModelsConfig.IsNull() {
		t.Fatal("models_config not applied")
	}
	eq, diags := m.ModelsConfig.StringSemanticEquals(context.Background(), modelsConfigFromRaw([]byte(config)))
	if diags.HasError() {
		t.Fatalf("semantic-equality diags: %v", diags)
	}
	if !eq {
		t.Errorf("applied read-back %q does not converge with weight-less config %q",
			m.ModelsConfig.ValueString(), config)
	}
}

// TestPolicyEvaluatorOptionsElisionKept proves that when the server elides an
// empty options object entirely, apply() keeps the operator's `{}` (matched from
// the plan/prior evaluators) rather than reading back null — the null-vs-non-null
// case semantic equality cannot bridge.
func TestPolicyEvaluatorOptionsElisionKept(t *testing.T) {
	r := &policyResource{}
	// m stands in for the plan/prior state: evaluator has an explicit `{}` options.
	m := policyResourceModel{
		Evaluators: []policyEvaluatorModel{{
			ID:      types.StringValue("ev_1"),
			Options: jsontypes.NewNormalizedValue(`{}`),
		}},
	}
	// Server read-back elides options entirely (nil).
	r.apply(&client.Policy{
		ID:         "pol_1",
		Evaluators: []client.EvaluatorRef{{ID: "ev_1", ExecuteOn: "input", Options: nil}},
	}, &m)

	if len(m.Evaluators) != 1 {
		t.Fatalf("expected 1 evaluator, got %d", len(m.Evaluators))
	}
	got := m.Evaluators[0].Options
	if got.IsNull() {
		t.Fatal("elided options with a non-null config must keep the config value, not read back null")
	}
	if got.ValueString() != `{}` {
		t.Errorf("expected kept options {}, got %q", got.ValueString())
	}

	// When the operator never set options (unknown on create), the elided read-back
	// collapses to a concrete null.
	m2 := policyResourceModel{
		Evaluators: []policyEvaluatorModel{{
			ID:      types.StringValue("ev_1"),
			Options: jsontypes.NewNormalizedUnknown(),
		}},
	}
	r.apply(&client.Policy{
		ID:         "pol_1",
		Evaluators: []client.EvaluatorRef{{ID: "ev_1", ExecuteOn: "input", Options: nil}},
	}, &m2)
	if !m2.Evaluators[0].Options.IsNull() {
		t.Errorf("omitted options must collapse to null, got %q", m2.Evaluators[0].Options.ValueString())
	}
}

// evalWithOptions builds a plan/state evaluator model with the given id and an
// options value (unknown when opts is the sentinel "<unknown>", null when "").
func evalWithOptions(id, opts string) policyEvaluatorModel {
	m := policyEvaluatorModel{ID: types.StringValue(id), ExecuteOn: types.StringValue("input")}
	switch opts {
	case "<unknown>":
		m.Options = jsontypes.NewNormalizedUnknown()
	case "":
		m.Options = jsontypes.NewNormalizedNull()
	default:
		m.Options = jsontypes.NewNormalizedValue(opts)
	}
	return m
}

// TestRetainEvaluatorOptionsOnUnrelatedUpdate proves the core MUST-FIX: options
// set on an evaluator survive an UNRELATED update (a rename) where the config
// omits options (planned unknown) — retained both in the outbound request and in
// the post-apply state, not wiped by the wholesale evaluator replace.
func TestRetainEvaluatorOptionsOnUnrelatedUpdate(t *testing.T) {
	prior := []policyEvaluatorModel{evalWithOptions("ev_A", `{"threshold":0.8}`)}
	// Plan for the rename: display_name changed elsewhere, options omitted -> unknown.
	plan := []policyEvaluatorModel{evalWithOptions("ev_A", "<unknown>")}

	retainEvaluatorOptions(plan, prior)

	// Retained in the model (so apply writes it to state).
	if got := plan[0].Options.ValueString(); got != `{"threshold":0.8}` {
		t.Fatalf("options must be retained in the plan model, got %q", got)
	}
	// Retained in the outbound request (the wholesale replace re-sends it).
	refs, diags := policyEvaluatorsFromModel(plan)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if len(refs) != 1 || refs[0].Options == nil {
		t.Fatalf("options must be present in the sent request, got %+v", refs)
	}
	if !reflect.DeepEqual(refs[0].Options, map[string]any{"threshold": 0.8}) {
		t.Errorf("sent options mismatch: %+v", refs[0].Options)
	}
	// Retained in state: server echoes non-empty options; apply writes them.
	r := &policyResource{}
	state := policyResourceModel{Evaluators: plan}
	r.apply(&client.Policy{
		ID:         "pol_1",
		Evaluators: []client.EvaluatorRef{{ID: "ev_A", ExecuteOn: "input", Options: map[string]any{"threshold": 0.8}}},
	}, &state)
	if got := state.Evaluators[0].Options.ValueString(); got != `{"threshold":0.8}` {
		t.Errorf("options must be retained in state, got %q", got)
	}
}

// TestRetainEvaluatorOptionsByID proves reconciliation matches evaluators by id
// (not position): reorder keeps each evaluator's own options, a removed evaluator
// drops entirely, and an added evaluator gets no options.
func TestRetainEvaluatorOptionsByID(t *testing.T) {
	optA, optB := `{"a":1}`, `{"b":2}`

	// Reorder: prior [A,B] -> plan [B,A], both options omitted (unknown).
	t.Run("reorder", func(t *testing.T) {
		prior := []policyEvaluatorModel{evalWithOptions("ev_A", optA), evalWithOptions("ev_B", optB)}
		plan := []policyEvaluatorModel{evalWithOptions("ev_B", "<unknown>"), evalWithOptions("ev_A", "<unknown>")}
		retainEvaluatorOptions(plan, prior)
		if plan[0].Options.ValueString() != optB {
			t.Errorf("reordered ev_B must keep its own options %q, got %q", optB, plan[0].Options.ValueString())
		}
		if plan[1].Options.ValueString() != optA {
			t.Errorf("reordered ev_A must keep its own options %q, got %q", optA, plan[1].Options.ValueString())
		}
	})

	// Removed: prior [A,B] -> plan [A]. B drops with the plan (never looked up).
	t.Run("removed", func(t *testing.T) {
		prior := []policyEvaluatorModel{evalWithOptions("ev_A", optA), evalWithOptions("ev_B", optB)}
		plan := []policyEvaluatorModel{evalWithOptions("ev_A", "<unknown>")}
		retainEvaluatorOptions(plan, prior)
		if len(plan) != 1 || plan[0].ID.ValueString() != "ev_A" {
			t.Fatalf("removed evaluator must be gone, got %+v", plan)
		}
		if plan[0].Options.ValueString() != optA {
			t.Errorf("kept ev_A must retain %q, got %q", optA, plan[0].Options.ValueString())
		}
		refs, _ := policyEvaluatorsFromModel(plan)
		if len(refs) != 1 || refs[0].ID != "ev_A" {
			t.Errorf("request must contain only ev_A, got %+v", refs)
		}
	})

	// Added: prior [A] -> plan [A, C]. C has no prior -> options stay null (unset).
	t.Run("added", func(t *testing.T) {
		prior := []policyEvaluatorModel{evalWithOptions("ev_A", optA)}
		plan := []policyEvaluatorModel{evalWithOptions("ev_A", "<unknown>"), evalWithOptions("ev_C", "<unknown>")}
		retainEvaluatorOptions(plan, prior)
		if plan[0].Options.ValueString() != optA {
			t.Errorf("ev_A must retain %q, got %q", optA, plan[0].Options.ValueString())
		}
		if !plan[1].Options.IsNull() {
			t.Errorf("added ev_C must have null (unset) options, got %q", plan[1].Options.ValueString())
		}
		refs, _ := policyEvaluatorsFromModel(plan)
		if len(refs) != 2 {
			t.Fatalf("expected 2 refs, got %d", len(refs))
		}
		if refs[1].Options != nil {
			t.Errorf("added ev_C must send no options, got %+v", refs[1].Options)
		}
	})
}

// TestRetainEvaluatorOptionsExplicitConfigUntouched proves an explicitly
// configured options (including an empty {}) is left exactly as the operator
// wrote it — retention only fills an omitted (unknown) options.
func TestRetainEvaluatorOptionsExplicitConfigUntouched(t *testing.T) {
	prior := []policyEvaluatorModel{evalWithOptions("ev_A", `{"a":1}`)}
	// Operator now sets options explicitly to {} (a known, non-unknown value).
	plan := []policyEvaluatorModel{evalWithOptions("ev_A", `{}`)}
	retainEvaluatorOptions(plan, prior)
	if plan[0].Options.ValueString() != `{}` {
		t.Errorf("explicit {} must not be overwritten by prior options, got %q", plan[0].Options.ValueString())
	}
}
