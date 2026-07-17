package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// jsonSemanticEqual reports whether two JSON strings are semantically equal under
// jsontypes.Normalized (the comparison Terraform uses for these attributes).
func jsonSemanticEqual(t *testing.T, a, b string) bool {
	t.Helper()
	eq, diags := jsontypes.NewNormalizedValue(a).StringSemanticEquals(context.Background(), jsontypes.NewNormalizedValue(b))
	if diags.HasError() {
		t.Fatalf("semantic-equality diags: %v", diags)
	}
	return eq
}

// TestModelsConfigWeightConverges proves a weight-less model config canonicalizes
// to the server's stored form (weight 0.5), so the plan value and the server
// read-back are semantically equal — no diff after apply.
func TestModelsConfigWeightConverges(t *testing.T) {
	config := `{"mode":"fallback","models":[{"model":"x"},{"model":"y","weight":0.25}]}`
	// What the server stores/returns: weight 0.5 injected for the model that
	// omitted it, the explicit 0.25 preserved.
	serverReadBack := `{"mode":"fallback","models":[{"model":"x","weight":0.5},{"model":"y","weight":0.25}]}`

	canon, ok := canonicalizeJSONObject(config, modelsConfigWeightCanon)
	if !ok {
		t.Fatal("canonicalize failed to parse config")
	}
	if !jsonSemanticEqual(t, canon.ValueString(), serverReadBack) {
		t.Errorf("canonicalized config %q does not converge with server read-back %q",
			canon.ValueString(), serverReadBack)
	}
}

// TestModelsConfigWeightCanonIdempotent proves canon(canon(x)) == canon(x).
func TestModelsConfigWeightCanonIdempotent(t *testing.T) {
	config := `{"mode":"weighted","models":[{"model":"x"},{"model":"z","weight":0}]}`
	first, _ := canonicalizeJSONObject(config, modelsConfigWeightCanon)
	second, _ := canonicalizeJSONObject(first.ValueString(), modelsConfigWeightCanon)
	if !jsonSemanticEqual(t, first.ValueString(), second.ValueString()) {
		t.Errorf("not idempotent: %q vs %q", first.ValueString(), second.ValueString())
	}
	// A zero weight is also coerced to 0.5.
	if !jsonSemanticEqual(t, first.ValueString(), `{"mode":"weighted","models":[{"model":"x","weight":0.5},{"model":"z","weight":0.5}]}`) {
		t.Errorf("zero weight not coerced: %q", first.ValueString())
	}
}

// TestRetryConfigOnCodesConverges proves an empty on_codes canonicalizes to the
// server's elided read-back.
func TestRetryConfigOnCodesConverges(t *testing.T) {
	for _, in := range []string{`{"count":3,"on_codes":[]}`, `{"count":3,"on_codes":null}`} {
		canon, ok := canonicalizeJSONObject(in, retryConfigOnCodesCanon)
		if !ok {
			t.Fatalf("canonicalize failed for %q", in)
		}
		if !jsonSemanticEqual(t, canon.ValueString(), `{"count":3}`) {
			t.Errorf("input %q did not converge with server read-back {\"count\":3}, got %q", in, canon.ValueString())
		}
	}
	// A populated on_codes is preserved.
	canon, _ := canonicalizeJSONObject(`{"count":3,"on_codes":[429,503]}`, retryConfigOnCodesCanon)
	if !jsonSemanticEqual(t, canon.ValueString(), `{"count":3,"on_codes":[429,503]}`) {
		t.Errorf("populated on_codes must be preserved, got %q", canon.ValueString())
	}
}

// TestOptionsEmptyToNull proves an empty options object canonicalizes to null.
func TestOptionsEmptyToNull(t *testing.T) {
	canon, ok := canonicalizeJSONObject(`{}`, optionsEmptyToNullCanon)
	if !ok {
		t.Fatal("canonicalize failed")
	}
	if !canon.IsNull() {
		t.Errorf("empty options must canonicalize to null, got %q", canon.ValueString())
	}
	// A populated options object is preserved.
	canon2, _ := canonicalizeJSONObject(`{"language":"en"}`, optionsEmptyToNullCanon)
	if canon2.IsNull() || !jsonSemanticEqual(t, canon2.ValueString(), `{"language":"en"}`) {
		t.Errorf("populated options must be preserved, got null=%v %q", canon2.IsNull(), canon2.ValueString())
	}
}

// TestJSONCanonPlanModifier exercises the plan-modifier plumbing: a set config is
// canonicalized into the plan; a null config either retains prior state or plans
// null depending on retainOnNull.
func TestJSONCanonPlanModifier(t *testing.T) {
	ctx := context.Background()
	mod := jsonCanonPlanModifier{fn: modelsConfigWeightCanon, retainOnNull: true}

	// Set config → canonical plan (weight injected).
	req := planmodifier.StringRequest{
		ConfigValue: types.StringValue(`{"mode":"fallback","models":[{"model":"x"}]}`),
		StateValue:  types.StringNull(),
		PlanValue:   types.StringValue(`{"mode":"fallback","models":[{"model":"x"}]}`),
	}
	resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
	mod.PlanModifyString(ctx, req, resp)
	if !jsonSemanticEqual(t, resp.PlanValue.ValueString(), `{"mode":"fallback","models":[{"model":"x","weight":0.5}]}`) {
		t.Errorf("plan not canonicalized: %q", resp.PlanValue.ValueString())
	}

	// Null config with retainOnNull → prior state retained (server PATCH cannot clear).
	prior := types.StringValue(`{"mode":"fallback","models":[{"model":"x","weight":0.5}]}`)
	reqNull := planmodifier.StringRequest{ConfigValue: types.StringNull(), StateValue: prior, PlanValue: types.StringNull()}
	respNull := &planmodifier.StringResponse{PlanValue: reqNull.PlanValue}
	mod.PlanModifyString(ctx, reqNull, respNull)
	if respNull.PlanValue.ValueString() != prior.ValueString() {
		t.Errorf("retainOnNull must keep prior state, got %q", respNull.PlanValue.ValueString())
	}

	// Null config without retainOnNull → plans null.
	modClear := jsonCanonPlanModifier{fn: optionsEmptyToNullCanon, retainOnNull: false}
	reqClear := planmodifier.StringRequest{ConfigValue: types.StringNull(), StateValue: prior, PlanValue: types.StringNull()}
	respClear := &planmodifier.StringResponse{PlanValue: reqClear.PlanValue}
	modClear.PlanModifyString(ctx, reqClear, respClear)
	if !respClear.PlanValue.IsNull() {
		t.Errorf("non-retaining modifier must plan null on null config, got %q", respClear.PlanValue.ValueString())
	}
}

// TestPolicyModelsConfigApplyConverges proves the full round-trip: the operator's
// canonicalized plan and the value apply() stores from the server read-back are
// semantically equal (no diff after apply).
func TestPolicyModelsConfigApplyConverges(t *testing.T) {
	config := `{"mode":"fallback","models":[{"model":"x"}]}`
	canonPlan, _ := canonicalizeJSONObject(config, modelsConfigWeightCanon)

	r := &policyResource{}
	var m policyResourceModel
	r.apply(&client.Policy{
		ID:           "pol_1",
		ModelsConfig: []byte(`{"mode":"fallback","models":[{"model":"x","weight":0.5}]}`),
	}, &m)

	if m.ModelsConfig.IsNull() {
		t.Fatal("models_config not applied")
	}
	if !jsonSemanticEqual(t, canonPlan.ValueString(), m.ModelsConfig.ValueString()) {
		t.Errorf("canonical plan %q does not converge with applied read-back %q",
			canonPlan.ValueString(), m.ModelsConfig.ValueString())
	}
}
