package provider

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// stringSlice converts a types.List of strings into a []string; null/unknown yields nil.
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

// knownListStrings returns the known string elements of a list. ok is false when the
// list or any element is null/unknown, so callers never compare against a
// placeholder.
func knownListStrings(l types.List) ([]string, bool) {
	if l.IsNull() || l.IsUnknown() {
		return nil, false
	}
	out := make([]string, 0, len(l.Elements()))
	for _, e := range l.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			return nil, false
		}
		out = append(out, s.ValueString())
	}
	return out, true
}

// preserveListOrder keeps the caller's element order when the server returns the
// SAME elements in a different one (endpoints that sort their output). Terraform
// compares a list positionally, so adopting the server's order after apply is an
// inconsistent result; a genuine membership change still takes the server's.
func preserveListOrder(prior types.List, srv []string) types.List {
	want, ok := knownListStrings(prior)
	if !ok || len(want) != len(srv) {
		return stringListValue(srv)
	}
	a, b := slices.Clone(want), slices.Clone(srv)
	slices.Sort(a)
	slices.Sort(b)
	if slices.Equal(a, b) {
		return prior
	}
	return stringListValue(srv)
}

// errDetail renders a normalized client error into a diagnostic detail.
func errDetail(err error) string {
	return "error [" + string(client.CodeOf(err)) + "]: " + err.Error()
}

// isNotFound reports whether the resource is gone server-side.
func isNotFound(err error) bool {
	return client.CodeOf(err) == client.CodeNotFound
}

// strPtr / boolPtr / int64Ptr / float64Ptr return a pointer to the attribute's
// value, or nil when it is null or unknown so the field is omitted from a
// sparse write.
func strPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func boolPtr(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	b := v.ValueBool()
	return &b
}

func int64Ptr(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	i := v.ValueInt64()
	return &i
}

func float64Ptr(v types.Float64) *float64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	f := v.ValueFloat64()
	return &f
}

// concreteBool collapses a null or unknown bool to false, for persisting a
// partial state (the framework rejects unknown values in post-apply state).
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
// or nil when null/unknown.
func normalizedToRaw(v jsontypes.Normalized) json.RawMessage {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return json.RawMessage(v.ValueString())
}

// rawToNormalized wraps raw JSON bytes into a jsontypes.Normalized value, whose
// semantic comparison keeps key order / whitespace from producing a diff.
func rawToNormalized(raw json.RawMessage) jsontypes.Normalized {
	if len(raw) == 0 {
		return jsontypes.NewNormalizedNull()
	}
	return jsontypes.NewNormalizedValue(string(raw))
}

// stringMap converts a types.Map of strings into a map[string]string; null/unknown yields nil.
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

// optStringPtr / optFloat64Ptr map an optional server field onto an attribute: a
// nil pointer is an absence (null), a present one is a value the server chose to
// store — including "" and 0.
func optStringPtr(v *string) types.String {
	if v == nil {
		return types.StringNull()
	}
	return types.StringValue(*v)
}

func optFloat64Ptr(v *float64) types.Float64 {
	if v == nil {
		return types.Float64Null()
	}
	return types.Float64Value(*v)
}
