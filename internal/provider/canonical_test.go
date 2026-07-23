package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
// lives entirely in the custom type's semantic equality. models_config
// additionally carries UseStateForUnknown (a churn-reduction that only
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
