package provider

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
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

// planWalker carries the re-validation across a schema. unhandled names the
// attribute kinds the walk does not know; it is always empty in production and
// is asserted on by the test that walks every registered resource, so a schema
// growing a new kind fails the build rather than silently losing coverage.
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
			w.strings(ctx, p, t.Validators)
		}
	case schema.BoolAttribute:
		if t.Optional || t.Required {
			w.bools(ctx, p, t.Validators)
		}
	case schema.Int64Attribute:
		if t.Optional || t.Required {
			w.int64s(ctx, p, t.Validators)
		}
	case schema.Float64Attribute:
		if t.Optional || t.Required {
			w.float64s(ctx, p, t.Validators)
		}
	case schema.ListAttribute:
		if t.Optional || t.Required {
			w.lists(ctx, p, t.Validators)
		}
	case schema.MapAttribute:
		if t.Optional || t.Required {
			w.maps(ctx, p, t.Validators)
		}
	case schema.SingleNestedAttribute:
		if t.Optional || t.Required {
			w.objects(ctx, p, t.Validators)
		}
		var v types.Object
		if w.value(ctx, p, &v) && !v.IsNull() && !v.IsUnknown() {
			w.attributes(ctx, p, t.Attributes)
		}
	case schema.ListNestedAttribute:
		if t.Optional || t.Required {
			w.lists(ctx, p, t.Validators)
		}
		w.eachElement(ctx, p, t.NestedObject.Attributes, nil)
	default:
		w.unhandled = append(w.unhandled, fmt.Sprintf("%s: %T", p, a))
	}
}

func (w *planWalker) blocks(ctx context.Context, parent path.Path, blocks map[string]schema.Block) {
	for _, name := range slices.Sorted(maps.Keys(blocks)) {
		p := parent.AtName(name)
		switch t := blocks[name].(type) {
		case schema.ListNestedBlock:
			w.lists(ctx, p, t.Validators)
			w.eachElement(ctx, p, t.NestedObject.Attributes, t.NestedObject.Blocks)
		default:
			w.unhandled = append(w.unhandled, fmt.Sprintf("%s: %T", p, blocks[name]))
		}
	}
}

// eachElement descends into every element of a list-shaped attribute or block.
func (w *planWalker) eachElement(ctx context.Context, p path.Path, attrs map[string]schema.Attribute, blocks map[string]schema.Block) {
	var l types.List
	if !w.value(ctx, p, &l) || l.IsNull() || l.IsUnknown() {
		return
	}
	for i := range l.Elements() {
		w.attributes(ctx, p.AtListIndex(i), attrs)
		w.blocks(ctx, p.AtListIndex(i), blocks)
	}
}

// value reads the planned value at p. A read that cannot be made (a path under a
// null or unknown parent) is skipped rather than reported: this pass only
// re-checks what the plan-time pass could not see.
func (w *planWalker) value(ctx context.Context, p path.Path, target any) bool {
	return !w.cfg.GetAttribute(ctx, p, target).HasError()
}

func (w *planWalker) strings(ctx context.Context, p path.Path, vs []validator.String) {
	var v types.String
	if len(vs) == 0 || !w.value(ctx, p, &v) {
		return
	}
	req := validator.StringRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: v}
	for _, val := range vs {
		resp := &validator.StringResponse{}
		val.ValidateString(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) bools(ctx context.Context, p path.Path, vs []validator.Bool) {
	var v types.Bool
	if len(vs) == 0 || !w.value(ctx, p, &v) {
		return
	}
	req := validator.BoolRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: v}
	for _, val := range vs {
		resp := &validator.BoolResponse{}
		val.ValidateBool(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) int64s(ctx context.Context, p path.Path, vs []validator.Int64) {
	var v types.Int64
	if len(vs) == 0 || !w.value(ctx, p, &v) {
		return
	}
	req := validator.Int64Request{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: v}
	for _, val := range vs {
		resp := &validator.Int64Response{}
		val.ValidateInt64(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) float64s(ctx context.Context, p path.Path, vs []validator.Float64) {
	var v types.Float64
	if len(vs) == 0 || !w.value(ctx, p, &v) {
		return
	}
	req := validator.Float64Request{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: v}
	for _, val := range vs {
		resp := &validator.Float64Response{}
		val.ValidateFloat64(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) lists(ctx context.Context, p path.Path, vs []validator.List) {
	var v types.List
	if len(vs) == 0 || !w.value(ctx, p, &v) {
		return
	}
	req := validator.ListRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: v}
	for _, val := range vs {
		resp := &validator.ListResponse{}
		val.ValidateList(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) maps(ctx context.Context, p path.Path, vs []validator.Map) {
	var v types.Map
	if len(vs) == 0 || !w.value(ctx, p, &v) {
		return
	}
	req := validator.MapRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: v}
	for _, val := range vs {
		resp := &validator.MapResponse{}
		val.ValidateMap(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}

func (w *planWalker) objects(ctx context.Context, p path.Path, vs []validator.Object) {
	var v types.Object
	if len(vs) == 0 || !w.value(ctx, p, &v) {
		return
	}
	req := validator.ObjectRequest{Path: p, PathExpression: p.Expression(), Config: w.cfg, ConfigValue: v}
	for _, val := range vs {
		resp := &validator.ObjectResponse{}
		val.ValidateObject(ctx, req, resp)
		w.diags.Append(resp.Diagnostics...)
	}
}
