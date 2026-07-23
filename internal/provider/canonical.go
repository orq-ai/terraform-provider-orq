package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/attr/xattr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// This file defines a custom string type for the opaque JSON-object attribute
// whose textual config differs from the server's stored read-back
// (models_config). It mirrors the proven rfc3339Instant pattern:
// a custom type whose StringSemanticEquals CANONICALIZES BOTH operands before
// comparing, so the operator's config and the server's read-back converge
// without ever rewriting the config value at plan time.
//
// Why not a plan modifier? A plan modifier that rewrites a NON-NULL config value
// (the previous approach) is rejected by Terraform's AssertPlanValid: for an
// Optional+Computed attribute the planned value may differ from config ONLY when
// config is null. Semantic equality has no such restriction — it suppresses the
// diff between plan/state and lets refresh keep the prior value, exactly like
// jsontypes.Normalized (whitespace/key-order) and rfc3339Instant (instant).
//
// The server-side defaulting this type absorbs:
//   - models_config: a model entry with an omitted or zero weight is stored with
//     weight 0.5 (routingrules/routes.go).
//
// The retain-on-null behavior (dropping the attribute from config keeps the
// prior server value with no diff) is NOT provided here — it comes from marking
// the attribute Optional+Computed, whose "sticky" plan value on a null config is
// the prior state. Semantic equality cannot reconcile null vs non-null and is
// never invoked for it.

// ---------------------------------------------------------------------------
// models_config: weight-0.5 canonicalizing JSON string type
// ---------------------------------------------------------------------------

type modelsConfigType struct {
	basetypes.StringType
}

var (
	_ basetypes.StringTypable                    = modelsConfigType{}
	_ basetypes.StringValuableWithSemanticEquals = modelsConfigValue{}
	_ xattr.ValidateableAttribute                = modelsConfigValue{}
)

func (t modelsConfigType) String() string { return "provider.modelsConfigType" }

func (t modelsConfigType) ValueType(context.Context) attr.Value { return modelsConfigValue{} }

func (t modelsConfigType) Equal(o attr.Type) bool {
	other, ok := o.(modelsConfigType)
	if !ok {
		return false
	}
	return t.StringType.Equal(other.StringType)
}

func (t modelsConfigType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return modelsConfigValue{StringValue: in}, nil
}

func (t modelsConfigType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	sv, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type of %T", attrValue)
	}
	valuable, diags := t.ValueFromString(ctx, sv)
	if diags.HasError() {
		return nil, fmt.Errorf("unexpected error converting StringValue to StringValuable: %v", diags)
	}
	return valuable, nil
}

type modelsConfigValue struct {
	basetypes.StringValue
}

func (v modelsConfigValue) Type(context.Context) attr.Type { return modelsConfigType{} }

func (v modelsConfigValue) Equal(o attr.Value) bool {
	other, ok := o.(modelsConfigValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals treats two model configs as equal when they are equal
// after every weight-less/zero-weight model entry is defaulted to weight 0.5 on
// BOTH sides — the value the server stores. Null/unknown fall back to exact
// equality (semantic equality is not meant to bridge null vs non-null).
func (v modelsConfigValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	o, ok := newValuable.(modelsConfigValue)
	if !ok {
		return false, diags
	}
	if v.IsNull() || v.IsUnknown() || o.IsNull() || o.IsUnknown() {
		return v.StringValue.Equal(o.StringValue), diags
	}
	return canonicalJSONEqual(v.ValueString(), o.ValueString(), modelsConfigCanon), diags
}

func (v modelsConfigValue) ValidateAttribute(_ context.Context, req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse) {
	validateJSONString(v.StringValue, req, resp)
	validateModelsConfigShape(v.StringValue, req, resp)
}

func modelsConfigFromRaw(raw json.RawMessage) modelsConfigValue {
	if len(raw) == 0 {
		return modelsConfigValue{StringValue: basetypes.NewStringNull()}
	}
	return modelsConfigValue{StringValue: basetypes.NewStringValue(string(raw))}
}

func (v modelsConfigValue) toRaw() json.RawMessage { return jsonStringToRaw(v.StringValue) }

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// canonicalJSONEqual reports whether a and b are equal after canon is applied to
// each (when it decodes to a JSON object) and both are re-serialized to a
// normalized form. It is a strict superset of jsontypes.Normalized equality
// (whitespace / key order / number text are all normalized identically), plus
// the canon defaulting. Non-JSON inputs fall back to exact string equality
// (ValidateAttribute already rejects invalid JSON, so this is defensive).
func canonicalJSONEqual(a, b string, canon func(map[string]any)) bool {
	na, oka := canonicalizeJSON(a, canon)
	nb, okb := canonicalizeJSON(b, canon)
	if !oka || !okb {
		return a == b
	}
	return na == nb
}

func canonicalizeJSON(s string, canon func(map[string]any)) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	// Preserve number text (429 stays 429, never 429.0) so equality matches
	// jsontypes.Normalized exactly for the parts canon does not touch.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if obj, ok := v.(map[string]any); ok {
		canon(obj)
		v = obj
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// modelsConfigCanon canonicalizes each model entry's weight so an operator's
// config converges with the server's float64 read-back. Mutates obj in place.
//
//   - An omitted or zero weight is defaulted to 0.5. The server marshals weight
//     with omitempty, so both an absent field and a literal 0 drop out of the
//     request and the server fills in its 0.5 default (routingrules/routes.go,
//     policies/routes.go) — collapsing 0 -> 0.5 here is therefore CORRECT, the
//     server treats weight:0 identically to an omitted weight.
//   - A present non-zero weight is re-emitted in canonical float64 spelling: the
//     transport round-trips weights as float64 and reads `0.50` back as `0.5`,
//     so we parse the number and let json.Marshal(float64) normalize the text
//     (0.50 / 0.500 / 5e-1 -> 0.5, 1 / 1.0 -> 1). WITHOUT this, the UseNumber
//     decoder preserves the literal `0.50` and a `weight = 0.50` config drifts
//     against the server's `0.5` read-back ("inconsistent result after apply").
//
// Only weight is float-normalized; other numbers keep their exact text.
func modelsConfigCanon(obj map[string]any) {
	models, ok := obj["models"].([]any)
	if !ok {
		return
	}
	for _, el := range models {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		if w, present := m["weight"]; !present || jsonNumberIsZero(w) {
			m["weight"] = 0.5
		} else if f, ok := jsonNumberAsFloat(w); ok {
			m["weight"] = f
		}
	}
}

// jsonNumberIsZero reports whether v is a JSON number equal to zero. With a
// UseNumber decoder, numbers arrive as json.Number; the plain float64 case is
// kept for callers that decode without UseNumber.
func jsonNumberIsZero(v any) bool {
	switch n := v.(type) {
	case float64:
		return n == 0
	case json.Number:
		f, err := n.Float64()
		return err == nil && f == 0
	}
	return false
}

// jsonNumberAsFloat parses v (a json.Number from a UseNumber decoder, or a plain
// float64) into a float64 so it can be re-emitted in canonical spelling. Storing
// a float64 back into the decoded map makes json.Marshal print the shortest
// round-trippable form (0.50 -> 0.5, 1.0 -> 1), matching the server's float64
// read-back. Reports false for non-numbers, leaving the value untouched.
func jsonNumberAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// jsonStringToRaw converts a JSON string attribute value to raw bytes, or nil
// when null/unknown (so the field is omitted from a sparse write).
func jsonStringToRaw(v basetypes.StringValue) json.RawMessage {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return json.RawMessage(v.ValueString())
}

// validateJSONString rejects a non-JSON value at plan time (mirrors
// jsontypes.Normalized.ValidateAttribute).
func validateJSONString(v basetypes.StringValue, req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	if !json.Valid([]byte(v.ValueString())) {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid JSON String Value",
			"A string value was provided that is not valid JSON string format (RFC 7159).\n\n"+
				"Given Value: "+v.ValueString()+"\n",
		)
	}
}

// ---------------------------------------------------------------------------
// plan-time shape validation
// ---------------------------------------------------------------------------
//
// validateJSONString only proves a value is SOME valid JSON. The client, however,
// decodes this attribute into a fixed Go struct (routing_rules.go):
// models_config -> restgen.ModelsConfig.
// A struct decode SILENTLY DROPS unknown keys and FAILS on a non-object top level
// or a wrong-typed known field — problems that otherwise surface only at apply (a
// dropped key reappears missing in the read-back as "inconsistent result after
// apply"; an array/scalar fails the decode). The validator below mirrors that
// struct shape so the same problems are caught at plan time, with a diagnostic
// that names the offending key. The canonicalization/semantic-equality above is
// untouched. The allowed key set is DERIVED from the restgen struct
// (mode + models{display_name,integration_id,model,weight}), not
// invented here.

// validateModelsConfigShape enforces the restgen.ModelsConfig object shape.
func validateModelsConfigShape(v basetypes.StringValue, req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse) {
	root, ok := decodeJSONForShape(v)
	if !ok {
		return
	}
	obj, ok := root.(map[string]any)
	if !ok {
		addShapeError(req, resp, fmt.Sprintf("models_config must be a JSON object, got %s", jsonValueType(root)))
		return
	}
	for key, val := range obj {
		switch key {
		case "mode":
			// restgen.ModelsConfig.Mode is a non-nullable string: a JSON null would
			// decode to "" and fail only at apply, so reject it here too.
			if !jsonIsString(val) {
				addShapeError(req, resp, fmt.Sprintf("models_config \"mode\" must be a string, got %s", jsonValueType(val)))
			}
		case "models":
			// restgen.ModelsConfig.Models is a nullable array of ModelRef.
			if val == nil {
				continue
			}
			arr, ok := val.([]any)
			if !ok {
				addShapeError(req, resp, fmt.Sprintf("models_config \"models\" must be an array, got %s", jsonValueType(val)))
				continue
			}
			for i, el := range arr {
				m, ok := el.(map[string]any)
				if !ok {
					addShapeError(req, resp, fmt.Sprintf("models_config models[%d] must be a JSON object, got %s", i, jsonValueType(el)))
					continue
				}
				validateModelRefShape(m, i, req, resp)
			}
		default:
			addUnknownKeyError(req, resp, "models_config", key, "mode, models")
		}
	}
}

// validateModelRefShape enforces the restgen.ModelRef object shape for one entry
// of models_config.models.
func validateModelRefShape(m map[string]any, i int, req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse) {
	for key, val := range m {
		switch key {
		case "display_name", "integration_id":
			// Nullable (*string) in restgen.ModelRef — null is a legitimate spelling
			// of "absent".
			if val != nil && !jsonIsString(val) {
				addShapeError(req, resp, fmt.Sprintf("models_config models[%d] %q must be a string, got %s", i, key, jsonValueType(val)))
			}
		case "model":
			// restgen.ModelRef.Model is a non-nullable string: a JSON null would
			// decode to "" and fail only at apply, so reject it here too.
			if !jsonIsString(val) {
				addShapeError(req, resp, fmt.Sprintf("models_config models[%d] \"model\" must be a string, got %s", i, jsonValueType(val)))
			}
		case "weight":
			if val != nil && !jsonIsNumber(val) {
				addShapeError(req, resp, fmt.Sprintf("models_config models[%d] \"weight\" must be a number, got %s", i, jsonValueType(val)))
			}
		default:
			addUnknownKeyError(req, resp, fmt.Sprintf("models_config models[%d]", i), key, "display_name, integration_id, model, weight")
		}
	}
}

// decodeJSONForShape decodes a known, non-null attribute value for shape checking
// using the same number-preserving decoder as canonicalizeJSON. It returns ok ==
// false for null/unknown (nothing to validate) or invalid JSON (validateJSONString
// already reported it).
func decodeJSONForShape(v basetypes.StringValue) (any, bool) {
	if v.IsNull() || v.IsUnknown() {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(v.ValueString()))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, false
	}
	return out, true
}

// jsonValueType names the JSON type of a decoded value (UseNumber decoder) for
// diagnostics.
func jsonValueType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	}
	return "unknown"
}

func jsonIsString(v any) bool { _, ok := v.(string); return ok }
func jsonIsNumber(v any) bool { _, ok := v.(json.Number); return ok }

// addShapeError reports a shape violation on the attribute.
func addShapeError(req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse, detail string) {
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid JSON Object Shape", detail)
}

// addUnknownKeyError reports a key the client's struct decode would silently drop.
func addUnknownKeyError(req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse, where, key, allowed string) {
	resp.Diagnostics.AddAttributeError(
		req.Path,
		"Unknown JSON Key",
		fmt.Sprintf("unknown key %q in %s; allowed keys are: %s. The server silently drops unknown keys, "+
			"which produces an inconsistent result after apply.", key, where, allowed),
	)
}
