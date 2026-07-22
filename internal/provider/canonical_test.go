package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
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

// TestCanonAttributesPlanModifiersDoNotRewriteConfig is the regression guard for
// the redesign: the broken approach used a plan modifier (jsonCanonPlanModifier)
// that rewrote a NON-NULL config value at plan time, which Terraform's
// AssertPlanValid rejects for a from-scratch weight-less create. The canon now
// lives entirely in the custom type's semantic equality. models_config /
// retry_config additionally carry UseStateForUnknown (a churn-reduction that only
// touches an UNKNOWN plan), so the guard is no longer "zero plan modifiers": it
// asserts that no plan modifier present rewrites a non-null config value.
func TestCanonAttributesPlanModifiersDoNotRewriteConfig(t *testing.T) {
	ctx := context.Background()

	var routing resource.SchemaResponse
	NewRoutingRuleResource().Schema(ctx, resource.SchemaRequest{}, &routing)
	assertNoPlanModifierRewritesNonNullConfig(t, routing.Schema.Attributes, "models_config")
	if _, ok := routing.Schema.Attributes["models_config"].(schema.StringAttribute).CustomType.(modelsConfigType); !ok {
		t.Error("routing_rule.models_config must use modelsConfigType")
	}

	var policy resource.SchemaResponse
	NewPolicyResource().Schema(ctx, resource.SchemaRequest{}, &policy)
	assertNoPlanModifierRewritesNonNullConfig(t, policy.Schema.Attributes, "models_config")
	assertNoPlanModifierRewritesNonNullConfig(t, policy.Schema.Attributes, "retry_config")
	if _, ok := policy.Schema.Attributes["models_config"].(schema.StringAttribute).CustomType.(modelsConfigType); !ok {
		t.Error("policy.models_config must use modelsConfigType")
	}
	if _, ok := policy.Schema.Attributes["retry_config"].(schema.StringAttribute).CustomType.(retryConfigType); !ok {
		t.Error("policy.retry_config must use retryConfigType")
	}

	// The nested evaluator `options` attribute carries no plan modifier at all —
	// its retention is handled in Update/apply, not by a modifier.
	block, ok := policy.Schema.Blocks["evaluators"].(schema.ListNestedBlock)
	if !ok {
		t.Fatal("policy evaluators is not a ListNestedBlock")
	}
	assertStringAttrNoPlanModifier(t, block.NestedObject.Attributes, "options")
	assertNoPlanModifierRewritesNonNullConfig(t, block.NestedObject.Attributes, "options")
}

func assertStringAttrNoPlanModifier(t *testing.T, attrs map[string]schema.Attribute, name string) {
	t.Helper()
	attr, ok := attrs[name].(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s is not a StringAttribute", name)
	}
	if len(attr.PlanModifiers) != 0 {
		t.Errorf("%s must have NO plan modifier, found %d", name, len(attr.PlanModifiers))
	}
}

// assertNoPlanModifierRewritesNonNullConfig runs every plan modifier on the named
// string attribute against a non-null configured value (plan == config, a
// different prior state) and asserts none of them changes the planned value. This
// is the property that matters: the OLD jsonCanonPlanModifier rewrote a non-null
// config (tripping AssertPlanValid), whereas UseStateForUnknown leaves a known
// plan untouched and only fills an unknown.
func assertNoPlanModifierRewritesNonNullConfig(t *testing.T, attrs map[string]schema.Attribute, name string) {
	t.Helper()
	attr, ok := attrs[name].(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s is not a StringAttribute", name)
	}
	ctx := context.Background()
	// A non-null configured value; the prior state differs (and is spelled
	// differently) so a canonicalizing rewrite would be observable.
	config := types.StringValue(`{"models":[{"model":"x","weight":0.50}]}`)
	state := types.StringValue(`{"models":[{"model":"x","weight":0.5}]}`)
	for i, pm := range attr.PlanModifiers {
		req := planmodifier.StringRequest{
			ConfigValue: config,
			PlanValue:   config,
			StateValue:  state,
		}
		resp := &planmodifier.StringResponse{PlanValue: config}
		pm.PlanModifyString(ctx, req, resp)
		if !resp.PlanValue.Equal(config) {
			t.Errorf("%s plan modifier #%d rewrote a non-null config value: %q -> %q",
				name, i, config.ValueString(), resp.PlanValue.ValueString())
		}
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
