package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/attr/xattr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// rfc3339InstantType is a string attribute type for an RFC 3339 timestamp whose
// semantic equality compares the INSTANT the value denotes, not its textual
// form. So a config value written in a non-UTC offset
// (2026-01-01T00:00:00+01:00) is semantically equal to the server's normalized
// UTC read-back (2025-12-31T23:00:00Z) and never produces a perpetual diff.
//
// It is a stricter analog of timetypes.RFC3339, which only folds a `Z` suffix
// against a literal `+00:00` offset (it compares reformatted strings, so
// differing offsets still diff). The provider owns this type so expires_at
// converges regardless of the offset the operator wrote.
type rfc3339InstantType struct {
	basetypes.StringType
}

var (
	_ basetypes.StringTypable                    = rfc3339InstantType{}
	_ basetypes.StringValuableWithSemanticEquals = rfc3339Instant{}
	_ xattr.ValidateableAttribute                = rfc3339Instant{}
)

func (t rfc3339InstantType) String() string { return "provider.rfc3339InstantType" }

func (t rfc3339InstantType) ValueType(_ context.Context) attr.Value { return rfc3339Instant{} }

func (t rfc3339InstantType) Equal(o attr.Type) bool {
	other, ok := o.(rfc3339InstantType)
	if !ok {
		return false
	}
	return t.StringType.Equal(other.StringType)
}

func (t rfc3339InstantType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return rfc3339Instant{StringValue: in}, nil
}

func (t rfc3339InstantType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
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

// rfc3339Instant is the value type for rfc3339InstantType.
type rfc3339Instant struct {
	basetypes.StringValue
}

func (v rfc3339Instant) Type(_ context.Context) attr.Type { return rfc3339InstantType{} }

func (v rfc3339Instant) Equal(o attr.Value) bool {
	other, ok := o.(rfc3339Instant)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals treats two timestamps as equal when they denote the same
// instant, so a differently-offset (or differently-formatted) input converges
// with the server's normalized UTC read-back.
func (v rfc3339Instant) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	newValue, ok := newValuable.(rfc3339Instant)
	if !ok {
		return false, diags
	}
	a, err1 := time.Parse(time.RFC3339, v.ValueString())
	b, err2 := time.Parse(time.RFC3339, newValue.ValueString())
	if err1 != nil || err2 != nil {
		// One side is not parseable; fall back to exact string equality.
		return v.ValueString() == newValue.ValueString(), diags
	}
	return a.Equal(b), diags
}

// ValidateAttribute rejects a non-RFC-3339 timestamp at plan time.
func (v rfc3339Instant) ValidateAttribute(_ context.Context, req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse) {
	if v.IsUnknown() || v.IsNull() {
		return
	}
	if _, err := time.Parse(time.RFC3339, v.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid RFC 3339 timestamp",
			"value must be an RFC 3339 timestamp: "+err.Error())
	}
}

// rfc3339InstantValue builds a known value; an empty string yields null.
func rfc3339InstantValue(s string) rfc3339Instant {
	if s == "" {
		return rfc3339Instant{StringValue: basetypes.NewStringNull()}
	}
	return rfc3339Instant{StringValue: basetypes.NewStringValue(s)}
}
