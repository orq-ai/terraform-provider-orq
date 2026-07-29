package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// stringSlice converts a types.List of strings into a []string. A null/unknown
// list yields nil.
func stringSlice(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := l.ElementsAs(ctx, &out, false)
	return out, diags
}

// stringListValue builds a types.List from a []string. A nil slice yields an
// empty (non-null) list so a server-normalized empty result never reads as null.
func stringListValue(ss []string) types.List {
	elems := make([]types.String, 0, len(ss))
	for _, s := range ss {
		elems = append(elems, types.StringValue(s))
	}
	l, _ := types.ListValueFrom(context.Background(), types.StringType, elems)
	return l
}

// errDetail renders a normalized client error into a diagnostic detail. It
// surfaces the transport-neutral code and message and never leaks a Connect
// service/method or REST route (the client seam guarantees that).
func errDetail(err error) string {
	return "error [" + string(client.CodeOf(err)) + "]: " + err.Error()
}

// isNotFound reports whether err normalized to a not-found code, i.e. the
// resource is gone server-side and should be dropped from state.
func isNotFound(err error) bool {
	return client.CodeOf(err) == client.CodeNotFound
}

// strPtr returns a pointer to the attribute's value, or nil when the attribute
// is null or unknown (so the field is omitted from a sparse write).
func strPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

// boolPtr returns a pointer to the attribute's value, or nil when null/unknown.
func boolPtr(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	b := v.ValueBool()
	return &b
}

// int64Ptr returns a pointer to the attribute's value, or nil when null/unknown.
func int64Ptr(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	i := v.ValueInt64()
	return &i
}

// float64Ptr returns a pointer to the attribute's value, or nil when null/unknown.
func float64Ptr(v types.Float64) *float64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	f := v.ValueFloat64()
	return &f
}

// concreteBool collapses a null or unknown bool to a known false, leaving a
// known true/false untouched. Used when persisting a partial (taint) state:
// the framework rejects unknown values in post-apply state, so every attribute
// must be concrete.
func concreteBool(b types.Bool) types.Bool {
	if b.IsNull() || b.IsUnknown() {
		return types.BoolValue(false)
	}
	return b
}

// optString maps an empty string to a null attribute, else a string value. Used
// for optional server fields that come back as "" when unset.
func optString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// normalizedToRaw converts a jsontypes.Normalized attribute into raw JSON bytes,
// or nil when the attribute is null/unknown (so the field is omitted from a
// sparse write).
func normalizedToRaw(v jsontypes.Normalized) json.RawMessage {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return json.RawMessage(v.ValueString())
}

// rawToNormalized wraps raw JSON bytes into a jsontypes.Normalized value, or a
// null when empty. jsontypes compares semantically, so key order / whitespace
// never produce a diff against the operator's config.
func rawToNormalized(raw json.RawMessage) jsontypes.Normalized {
	if len(raw) == 0 {
		return jsontypes.NewNormalizedNull()
	}
	return jsontypes.NewNormalizedValue(string(raw))
}

// stringMap converts a types.Map of strings into a map[string]string. A
// null/unknown map yields nil.
func stringMap(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return nil, nil
	}
	out := make(map[string]string, len(m.Elements()))
	diags := m.ElementsAs(ctx, &out, false)
	return out, diags
}

// stringMapValue builds a types.Map from a map[string]string. A nil/empty map
// yields a null map so a server that omits the field never reads as an empty
// (non-null) map and produces a diff.
func stringMapValue(m map[string]string) types.Map {
	if len(m) == 0 {
		return types.MapNull(types.StringType)
	}
	elems := make(map[string]types.String, len(m))
	for k, v := range m {
		elems[k] = types.StringValue(v)
	}
	out, _ := types.MapValueFrom(context.Background(), types.StringType, elems)
	return out
}

// optStringPtr maps an optional server string onto an attribute: a nil pointer
// (the field is absent from the response) yields null, a present pointer yields
// its value verbatim — including "" , which is a value the server chose to
// store, not an absence.
func optStringPtr(v *string) types.String {
	if v == nil {
		return types.StringNull()
	}
	return types.StringValue(*v)
}

// optFloat64Ptr mirrors optStringPtr for an optional server float (a nil pointer
// is an absent field, not a 0).
func optFloat64Ptr(v *float64) types.Float64 {
	if v == nil {
		return types.Float64Null()
	}
	return types.Float64Value(*v)
}
