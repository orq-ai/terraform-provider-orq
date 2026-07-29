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

// models_config is an opaque JSON-object attribute whose textual config differs
// from the server's stored read-back. Following rfc3339Instant, this is a custom
// string type whose StringSemanticEquals canonicalizes BOTH operands before
// comparing, so config and read-back converge without rewriting the config value
// at plan time.

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

// StringSemanticEquals compares both sides canonicalized. Null/unknown fall back
// to exact equality: semantic equality cannot bridge null vs non-null.
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

// canonicalJSONEqual is jsontypes.Normalized equality (whitespace, key order and
// number text) plus canon's defaulting. Non-JSON falls back to string equality.
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
	// Preserve number text (429 stays 429, never 429.0).
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

// modelsConfigCanon rewrites each model entry's weight in place so config
// converges with the server's float64 read-back. The server marshals weight with
// omitempty, so a literal 0 drops out of the request exactly like an absent
// field and comes back as the server's 0.5 default. A present non-zero weight is
// re-emitted as a float64 so json.Marshal normalizes its text (0.50 -> 0.5).
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

// jsonNumberIsZero reports whether v is a JSON number equal to zero.
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

// jsonNumberAsFloat parses v into a float64, reporting false for non-numbers.
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
// when null/unknown.
func jsonStringToRaw(v basetypes.StringValue) json.RawMessage {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return json.RawMessage(v.ValueString())
}

// validateJSONString rejects a non-JSON value at plan time.
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
// The client decodes this attribute into restgen.ModelsConfig, and a struct
// decode SILENTLY DROPS unknown keys and fails on a wrong-typed known field —
// problems that otherwise surface only at apply. The checks below mirror that
// struct shape (its fields ARE the allowed key set) so they are caught at plan
// time with a diagnostic naming the offending key.

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
			// Non-nullable in restgen: a JSON null would decode to "".
			if !jsonIsString(val) {
				addShapeError(req, resp, fmt.Sprintf("models_config \"mode\" must be a string, got %s", jsonValueType(val)))
			}
		case "models":
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

func validateModelRefShape(m map[string]any, i int, req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse) {
	for key, val := range m {
		switch key {
		case "display_name", "integration_id":
			// Nullable in restgen: null is a legitimate spelling of "absent".
			if val != nil && !jsonIsString(val) {
				addShapeError(req, resp, fmt.Sprintf("models_config models[%d] %q must be a string, got %s", i, key, jsonValueType(val)))
			}
		case "model":
			// Non-nullable in restgen: a JSON null would decode to "".
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

// decodeJSONForShape returns ok == false for null/unknown (nothing to validate)
// or invalid JSON (validateJSONString already reported it).
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

// jsonValueType names the JSON type of a decoded value, for diagnostics.
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

func addShapeError(req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse, detail string) {
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid JSON Object Shape", detail)
}

func addUnknownKeyError(req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse, where, key, allowed string) {
	resp.Diagnostics.AddAttributeError(
		req.Path,
		"Unknown JSON Key",
		fmt.Sprintf("unknown key %q in %s; allowed keys are: %s. The server silently drops unknown keys, "+
			"which produces an inconsistent result after apply.", key, where, allowed),
	)
}
