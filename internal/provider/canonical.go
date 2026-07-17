package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// This file canonicalizes opaque JSON-object attributes (models_config,
// retry_config, evaluator options) to the server's stored representation at plan
// time, so an operator's config and the server's read-back converge instead of
// producing a perpetual diff or an "inconsistent result after apply" error.
//
// The server applies defaulting/eliding the provider cannot see through the
// jsontypes.Normalized blob:
//   - models_config: a model with omitted/zero weight is stored with weight 0.5
//     (routingrules/routes.go, policies/routes.go).
//   - retry_config: an empty/absent on_codes is elided (omitempty) on read.
//   - evaluator options: an empty object is elided (omitempty) on read → null.
//
// Because a plan modifier here deliberately produces a plan value that differs
// from the raw config value, every attribute using one MUST be Computed —
// Terraform only permits a non-config plan value for Computed attributes.

// jsonObjectCanon transforms a decoded JSON object into its server-canonical
// form. A true second return means the value canonicalizes to null. It must be
// pure and idempotent (canon(canon(x)) == canon(x)).
type jsonObjectCanon func(map[string]any) (obj map[string]any, isNull bool)

// jsonCanonPlanModifier canonicalizes a jsontypes.Normalized (JSON object
// string) attribute at plan time via fn.
type jsonCanonPlanModifier struct {
	fn jsonObjectCanon
	// retainOnNull keeps the prior state value when the config is null, for
	// fields the server PATCH cannot clear (a nil in the sparse update means
	// "keep"). When false, a null config plans as null.
	retainOnNull bool
	description  string
}

func (m jsonCanonPlanModifier) Description(context.Context) string         { return m.description }
func (m jsonCanonPlanModifier) MarkdownDescription(context.Context) string { return m.description }

func (m jsonCanonPlanModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.ConfigValue.IsNull() {
		if m.retainOnNull {
			resp.PlanValue = req.StateValue
		}
		return
	}
	if req.ConfigValue.IsUnknown() {
		return
	}
	canon, ok := canonicalizeJSONObject(req.ConfigValue.ValueString(), m.fn)
	if !ok {
		// Not a JSON object: leave the plan as the config value so the downstream
		// decode/validation surfaces the real error rather than this modifier.
		return
	}
	resp.PlanValue = canon
}

// canonicalizeJSONObject decodes s as a JSON object, applies fn, and returns the
// canonical value as a plain string value (the framework re-wraps it into the
// attribute's custom type). ok is false when s is not a JSON object.
func canonicalizeJSONObject(s string, fn jsonObjectCanon) (types.String, bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return types.StringNull(), false
	}
	out, isNull := fn(obj)
	if isNull {
		return types.StringNull(), true
	}
	b, err := json.Marshal(out)
	if err != nil {
		return types.StringNull(), false
	}
	return types.StringValue(string(b)), true
}

// modelsConfigWeightCanon injects the server default weight (0.5) for any model
// whose weight is omitted or zero, mirroring the server. Never null.
func modelsConfigWeightCanon(obj map[string]any) (map[string]any, bool) {
	models, ok := obj["models"].([]any)
	if !ok {
		return obj, false
	}
	for _, el := range models {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		if w, present := m["weight"]; !present || jsonNumberIsZero(w) {
			m["weight"] = 0.5
		}
	}
	return obj, false
}

// retryConfigOnCodesCanon drops an empty or null on_codes so it matches the
// server's elided (omitempty) read-back. Never null.
func retryConfigOnCodesCanon(obj map[string]any) (map[string]any, bool) {
	if v, present := obj["on_codes"]; present {
		if v == nil {
			delete(obj, "on_codes")
		} else if arr, ok := v.([]any); ok && len(arr) == 0 {
			delete(obj, "on_codes")
		}
	}
	return obj, false
}

// optionsEmptyToNullCanon canonicalizes an empty options object to null so it
// matches the server's elided (omitempty) read-back.
func optionsEmptyToNullCanon(obj map[string]any) (map[string]any, bool) {
	if len(obj) == 0 {
		return nil, true
	}
	return obj, false
}

// jsonNumberIsZero reports whether v is a JSON number equal to zero. encoding/json
// decodes numbers into float64 by default.
func jsonNumberIsZero(v any) bool {
	f, ok := v.(float64)
	return ok && f == 0
}
