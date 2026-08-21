package provider

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// revalidatePlan re-runs every schema validator against the RESOLVED plan.
//
// The framework runs attribute validators once, at validate/plan time, and each
// one SKIPS a value that is unknown then; it never re-runs them at apply. So an
// interpolated value that resolves to something the validator would have
// rejected — a lowercase enum, an empty id, a padded name — reaches the server
// unchecked and comes back normalized, which Terraform reports as an
// inconsistent result on an already-created resource.
//
// Only WRITABLE attributes are re-checked. A Computed-only attribute never
// carries a config value at all, so validating one would judge the server's own
// output; everything the operator can write is either the config value itself or
// a canonical default the validators already accept.
func revalidatePlan(ctx context.Context, plan tfsdk.Plan) diag.Diagnostics {
	s, ok := plan.Schema.(schema.Schema)
	if !ok {
		return nil
	}
	w := &planWalker{cfg: tfsdk.Config{Schema: plan.Schema, Raw: plan.Raw}}
	w.attributes(ctx, path.Empty(), s.Attributes)
	w.blocks(ctx, path.Empty(), s.Blocks)
	return w.diags
}

// planWalker carries the re-validation across a schema. unhandled names every
// place the walk could not do its job — an attribute kind it does not know, or a
// value it could not read — and is always empty in production. The test that
// walks every registered resource asserts on it, so a schema that outgrows the
// walker fails the build instead of silently losing coverage.
type planWalker struct {
	cfg       tfsdk.Config
	diags     diag.Diagnostics
	unhandled []string
}

func (w *planWalker) attributes(ctx context.Context, parent path.Path, attrs map[string]schema.Attribute) {
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		w.attribute(ctx, parent.AtName(name), attrs[name])
	}
}

func (w *planWalker) attribute(ctx context.Context, p path.Path, a schema.Attribute) {
	switch t := a.(type) {
	case schema.StringAttribute:
		if t.Optional || t.Required {
			w.strings(ctx, p, a, t.Validators)
		}
	case schema.BoolAttribute:
		if t.Optional || t.Required {
			w.bools(ctx, p, a, t.Validators)
		}
	case schema.Int64Attribute:
		if t.Optional || t.Required {
			w.int64s(ctx, p, a, t.Validators)
		}
	case schema.Float64Attribute:
		if t.Optional || t.Required {
			w.float64s(ctx, p, a, t.Validators)
		}
	case schema.ListAttribute:
		if t.Optional || t.Required {
			w.lists(ctx, p, a, t.Validators)
		}
	case schema.MapAttribute:
		if t.Optional || t.Required {
			w.maps(ctx, p, a, t.Validators)
		}
	case schema.SingleNestedAttribute:
		if t.Optional || t.Required {
			w.objects(ctx, p, a, t.Validators)
		}
		if v, ok := w.object(ctx, p, a.GetType()); ok && !v.IsNull() && !v.IsUnknown() {
			w.attributes(ctx, p, t.Attributes)
		}
	case schema.ListNestedAttribute:
		if t.Optional || t.Required {
			w.lists(ctx, p, a, t.Validators)
		}
		w.eachElement(ctx, p, a.GetType(), t.NestedObject.Attributes, nil)
	default:
		w.unhandled = append(w.unhandled, fmt.Sprintf("%s: unsupported kind %T", p, a))
	}
}

func (w *planWalker) blocks(ctx context.Context, parent path.Path, blocks map[string]schema.Block) {
	for _, name := range slices.Sorted(maps.Keys(blocks)) {
		p := parent.AtName(name)
		switch t := blocks[name].(type) {
		case schema.ListNestedBlock:
			w.listValidators(ctx, p, t.Type(), t.Validators)
			w.eachElement(ctx, p, t.Type(), t.NestedObject.Attributes, t.NestedObject.Blocks)
		default:
			w.unhandled = append(w.unhandled, fmt.Sprintf("%s: unsupported kind %T", p, blocks[name]))
		}
	}
}

// eachElement descends into every element of a list-shaped attribute or block.
func (w *planWalker) eachElement(ctx context.Context, p path.Path, typ attr.Type, attrs map[string]schema.Attribute, blocks map[string]schema.Block) {
	l, ok := w.list(ctx, p, typ)
	if !ok || l.IsNull() || l.IsUnknown() {
		return
	}
	for i := range l.Elements() {
		w.attributes(ctx, p.AtListIndex(i), attrs)
		w.blocks(ctx, p.AtListIndex(i), blocks)
	}
}

// planned reads the value at p as the attribute's OWN type, which is the only
// way a CUSTOM-typed attribute can be read at all: reading one into the base
// type it wraps fails, and a failure that is passed over silently removes the
// attribute from this pass without anything saying so.
//
// ok is false when there is simply nothing at that path — an element index that
// does not exist, or an attribute under a null nested object — which is a
// legitimate absence and not recorded. A value that IS there but cannot be
// converted is recorded, because that is a hole in the pass.
func (w *planWalker) planned(ctx context.Context, p path.Path, typ attr.Type) (attr.Value, bool) {
	tfPath, ok := terraformPath(p)
	if !ok {
		w.unreadable(p, "the path cannot be expressed for the raw plan")
		return nil, false
	}
	raw, remaining, err := tftypes.WalkAttributePath(w.cfg.Raw, tfPath)
	if err != nil || len(remaining.Steps()) > 0 {
		return nil, false
	}
	v, ok := raw.(tftypes.Value)
	if !ok {
		return nil, false
	}
	out, err := typ.ValueFromTerraform(ctx, v)
	if err != nil {
		w.unreadable(p, err.Error())
		return nil, false
	}
	return out, true
}

func (w *planWalker) unreadable(p path.Path, why string) {
	w.unhandled = append(w.unhandled, fmt.Sprintf("%s: unreadable (%s)", p, why))
}

// terraformPath converts a framework path into the raw-value path that walks
// the plan. Only the step kinds this walk produces are supported.
func terraformPath(p path.Path) (*tftypes.AttributePath, bool) {
	out := tftypes.NewAttributePath()
	for _, step := range p.Steps() {
		switch s := step.(type) {
		case path.PathStepAttributeName:
			out = out.WithAttributeName(string(s))
		case path.PathStepElementKeyInt:
			out = out.WithElementKeyInt(int(s))
		case path.PathStepElementKeyString:
			out = out.WithElementKeyString(string(s))
		default:
			return nil, false
		}
	}
	return out, true
}

// The narrowing helpers below mirror what the framework does before it calls a
// typed validator: take the attribute's own value and reduce it to the base type
// the request carries, so a custom type is validated exactly like the primitive
// it wraps.

func (w *planWalker) object(ctx context.Context, p path.Path, typ attr.Type) (types.Object, bool) {
	v, ok := w.planned(ctx, p, typ)
	if !ok {
		return types.ObjectNull(nil), false
	}
	valuable, ok := v.(basetypes.ObjectValuable)
	if !ok {
		w.unreadable(p, fmt.Sprintf("%T is not an object value", v))
		return types.ObjectNull(nil), false
	}
	out, diags := valuable.ToObjectValue(ctx)
	if diags.HasError() {
		w.unreadable(p, "conversion to an object value failed")
		return types.ObjectNull(nil), false
	}
	return out, true
}

func (w *planWalker) list(ctx context.Context, p path.Path, typ attr.Type) (types.List, bool) {
	v, ok := w.planned(ctx, p, typ)
	if !ok {
		return types.ListNull(nil), false
	}
	valuable, ok := v.(basetypes.ListValuable)
	if !ok {
		w.unreadable(p, fmt.Sprintf("%T is not a list value", v))
		return types.ListNull(nil), false
	}
	out, diags := valuable.ToListValue(ctx)
	if diags.HasError() {
		w.unreadable(p, "conversion to a list value failed")
		return types.ListNull(nil), false
	}
	return out, true
}

func (w *planWalker) strings(ctx context.Context, p path.Path, a schema.Attribute, vs []validator.String) {
	if len(vs) == 0 {
		return
	}
	v, ok := w.planned(ctx, p, a.GetType())
	if !ok {
		return
	}
	valuable, ok := v.(basetypes.StringValuable)
	if !ok {
		w.unreadable(p, fmt.Sprintf("%T is not a string value", v))
		return
	}
	cv, diags := valuable.ToStringValue(ctx)
	if diags.HasError() {
		w.unreadable(p, "conversion to a string value failed")
		return
	}
	req := validator.StringRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: cv}
	for _, val := range vs {
		resp := &validator.StringResponse{}
		val.ValidateString(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) bools(ctx context.Context, p path.Path, a schema.Attribute, vs []validator.Bool) {
	if len(vs) == 0 {
		return
	}
	v, ok := w.planned(ctx, p, a.GetType())
	if !ok {
		return
	}
	valuable, ok := v.(basetypes.BoolValuable)
	if !ok {
		w.unreadable(p, fmt.Sprintf("%T is not a bool value", v))
		return
	}
	cv, diags := valuable.ToBoolValue(ctx)
	if diags.HasError() {
		w.unreadable(p, "conversion to a bool value failed")
		return
	}
	req := validator.BoolRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: cv}
	for _, val := range vs {
		resp := &validator.BoolResponse{}
		val.ValidateBool(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) int64s(ctx context.Context, p path.Path, a schema.Attribute, vs []validator.Int64) {
	if len(vs) == 0 {
		return
	}
	v, ok := w.planned(ctx, p, a.GetType())
	if !ok {
		return
	}
	valuable, ok := v.(basetypes.Int64Valuable)
	if !ok {
		w.unreadable(p, fmt.Sprintf("%T is not an int64 value", v))
		return
	}
	cv, diags := valuable.ToInt64Value(ctx)
	if diags.HasError() {
		w.unreadable(p, "conversion to an int64 value failed")
		return
	}
	req := validator.Int64Request{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: cv}
	for _, val := range vs {
		resp := &validator.Int64Response{}
		val.ValidateInt64(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) float64s(ctx context.Context, p path.Path, a schema.Attribute, vs []validator.Float64) {
	if len(vs) == 0 {
		return
	}
	v, ok := w.planned(ctx, p, a.GetType())
	if !ok {
		return
	}
	valuable, ok := v.(basetypes.Float64Valuable)
	if !ok {
		w.unreadable(p, fmt.Sprintf("%T is not a float64 value", v))
		return
	}
	cv, diags := valuable.ToFloat64Value(ctx)
	if diags.HasError() {
		w.unreadable(p, "conversion to a float64 value failed")
		return
	}
	req := validator.Float64Request{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: cv}
	for _, val := range vs {
		resp := &validator.Float64Response{}
		val.ValidateFloat64(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) lists(ctx context.Context, p path.Path, a schema.Attribute, vs []validator.List) {
	w.listValidators(ctx, p, a.GetType(), vs)
}

func (w *planWalker) listValidators(ctx context.Context, p path.Path, typ attr.Type, vs []validator.List) {
	if len(vs) == 0 {
		return
	}
	cv, ok := w.list(ctx, p, typ)
	if !ok {
		return
	}
	req := validator.ListRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: cv}
	for _, val := range vs {
		resp := &validator.ListResponse{}
		val.ValidateList(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) maps(ctx context.Context, p path.Path, a schema.Attribute, vs []validator.Map) {
	if len(vs) == 0 {
		return
	}
	v, ok := w.planned(ctx, p, a.GetType())
	if !ok {
		return
	}
	valuable, ok := v.(basetypes.MapValuable)
	if !ok {
		w.unreadable(p, fmt.Sprintf("%T is not a map value", v))
		return
	}
	cv, diags := valuable.ToMapValue(ctx)
	if diags.HasError() {
		w.unreadable(p, "conversion to a map value failed")
		return
	}
	req := validator.MapRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: cv}
	for _, val := range vs {
		resp := &validator.MapResponse{}
		val.ValidateMap(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) objects(ctx context.Context, p path.Path, a schema.Attribute, vs []validator.Object) {
	if len(vs) == 0 {
		return
	}
	cv, ok := w.object(ctx, p, a.GetType())
	if !ok {
		return
	}
	req := validator.ObjectRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: cv}
	for _, val := range vs {
		resp := &validator.ObjectResponse{}
		val.ValidateObject(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}
