package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// modelsConfigEqual is the comparison Terraform runs for the attribute.
func modelsConfigEqual(t *testing.T, a, b string) bool {
	t.Helper()
	eq, diags := modelsConfigFromRaw([]byte(a)).StringSemanticEquals(context.Background(), modelsConfigFromRaw([]byte(b)))
	if diags.HasError() {
		t.Fatalf("models_config semantic-equality diags: %v", diags)
	}
	return eq
}

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
	// null vs a value -> NOT equal; retain-on-null comes from Computed, not here.
	eq, _ = nullV.StringSemanticEquals(context.Background(), modelsConfigFromRaw([]byte(`{"models":[]}`)))
	if eq {
		t.Error("null vs non-null must NOT be semantically equal")
	}
}

// Rewriting a NON-NULL config value at plan time is what AssertPlanValid rejects;
// UseStateForUnknown is fine because it only touches an unknown plan.
func TestCanonAttributesPlanModifiersDoNotRewriteConfig(t *testing.T) {
	ctx := context.Background()

	var routing resource.SchemaResponse
	NewRoutingRuleResource().Schema(ctx, resource.SchemaRequest{}, &routing)
	assertNoPlanModifierRewritesNonNullConfig(t, routing.Schema.Attributes, "models_config")
	if _, ok := routing.Schema.Attributes["models_config"].(schema.StringAttribute).CustomType.(modelsConfigType); !ok {
		t.Error("routing_rule.models_config must use modelsConfigType")
	}
}

func assertNoPlanModifierRewritesNonNullConfig(t *testing.T, attrs map[string]schema.Attribute, name string) {
	t.Helper()
	attr, ok := attrs[name].(schema.StringAttribute)
	if !ok {
		t.Fatalf("%s is not a StringAttribute", name)
	}
	ctx := context.Background()
	// The prior state differs, and is spelled differently, so a canonicalizing
	// rewrite would be observable.
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
