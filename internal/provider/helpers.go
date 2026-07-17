package provider

import (
	"context"

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

// optString maps an empty string to a null attribute, else a string value. Used
// for optional server fields that come back as "" when unset.
func optString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
