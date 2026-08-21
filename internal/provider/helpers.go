package provider

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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

// preserveEmptyList keeps the configured empty-vs-absent shape when the server
// returns no elements: a KNOWN empty list stays [], an absent one stays null.
// The wire cannot tell the two apart, and collapsing [] to null is an
// inconsistent result the operator can never converge away from.
func preserveEmptyList(prior types.List, srv []string) types.List {
	if len(srv) > 0 {
		return preserveListOrder(prior, srv)
	}
	if prior.IsNull() || prior.IsUnknown() {
		return types.ListNull(types.StringType)
	}
	return stringListValue(nil)
}

// preserveEmptyString is the same idea for a string the server returns as "":
// a KNOWN empty planned/prior value is kept rather than collapsed to null.
func preserveEmptyString(prior types.String, srv string) types.String {
	if srv != "" {
		return types.StringValue(srv)
	}
	if !prior.IsNull() && !prior.IsUnknown() && prior.ValueString() == "" {
		return prior
	}
	return types.StringNull()
}

// uniqueNonNullStrings rejects the element shapes a string-list write cannot
// survive: a repeated value (a server that stores a set drops it, or 400s) and a
// null element (ElementsAs fails). Unknown elements are left to the plan-time
// validator, which is the only place they can exist.
func uniqueNonNullStrings(l types.List, attribute path.Path, summary, duplicateDetail, nullDetail string) diag.Diagnostics {
	var diags diag.Diagnostics
	if l.IsNull() || l.IsUnknown() {
		return diags
	}
	seen := make(map[string]bool, len(l.Elements()))
	for i, e := range l.Elements() {
		v, ok := e.(types.String)
		if !ok || v.IsUnknown() {
			continue
		}
		switch {
		case v.IsNull():
			diags.AddAttributeError(attribute.AtListIndex(i), summary, nullDetail)
		case seen[v.ValueString()]:
			diags.AddAttributeError(attribute.AtListIndex(i), summary, duplicateDetail)
		default:
			seen[v.ValueString()] = true
		}
	}
	return diags
}

// nonEmptyStringValidator rejects an explicitly empty string for an attribute
// where "" carries no meaning: the server stores it as absence and reads it back
// as null, which fails the apply with an inconsistent result on an already
// created resource. The remedy is per-attribute, so the caller supplies it.
type nonEmptyStringValidator struct{ remedy string }

func (nonEmptyStringValidator) Description(context.Context) string {
	return "must not be an empty string"
}

func (v nonEmptyStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v nonEmptyStringValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.ConfigValue.ValueString() != "" {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid empty value", emptyValueDetail(v.remedy))
}

// nonEmptyMapValidator is the same rule for a map the server cannot store empty.
type nonEmptyMapValidator struct{ remedy string }

func (nonEmptyMapValidator) Description(context.Context) string {
	return "must not be an empty map"
}

func (v nonEmptyMapValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v nonEmptyMapValidator) ValidateMap(_ context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || len(req.ConfigValue.Elements()) > 0 {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid empty value", emptyValueDetail(v.remedy))
}

func emptyValueDetail(remedy string) string {
	return "An empty value means nothing here: the server stores it as absence and reads it back as null, " +
		"which fails the apply with an inconsistent result. " + remedy
}

// uniqueStringsValidator is uniqueNonNullStrings as a schema validator, so the
// rule is enforced at plan time AND — via revalidatePlan — again at apply on the
// resolved value. The messages are per-attribute because the consequence is.
type uniqueStringsValidator struct {
	summary         string
	duplicateDetail string
	nullDetail      string
}

func (uniqueStringsValidator) Description(context.Context) string {
	return "must contain unique, non-null values"
}

func (v uniqueStringsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v uniqueStringsValidator) ValidateList(_ context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	resp.Diagnostics.Append(uniqueNonNullStrings(req.ConfigValue, req.Path, v.summary, v.duplicateDetail, v.nullDetail)...)
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
