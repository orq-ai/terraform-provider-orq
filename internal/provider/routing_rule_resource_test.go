package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// --- a routing-rules API that reproduces the server's storage rules ----------

// fakeRoutingRules mirrors apps/platform-api/routingrules/connect_routes.go:
// modelsConfigFromProto substitutes weight 0.5 for an absent or zero weight and
// stores the remaining leaves verbatim, and the response marshaler emits proto
// zero values, so unset leaf strings come back as "".
type fakeRoutingRules struct {
	rule    client.RoutingRule
	created *client.RoutingRuleCreateInput
	updated *client.RoutingRuleUpdateInput
}

func storedModelsConfig(c *client.RoutingRuleModelsConfig) *client.RoutingRuleModelsConfig {
	if c == nil {
		return nil
	}
	out := &client.RoutingRuleModelsConfig{Mode: c.Mode}
	for _, m := range c.Models {
		weight := 0.5
		if m.Weight != nil && *m.Weight != 0 {
			weight = *m.Weight
		}
		out.Models = append(out.Models, client.RoutingRuleModelRef{
			Model:         m.Model,
			DisplayName:   m.DisplayName,
			Weight:        &weight,
			IntegrationID: m.IntegrationID,
		})
	}
	return out
}

func (f *fakeRoutingRules) List(context.Context, client.ListParams) (*client.RoutingRulePage, error) {
	return &client.RoutingRulePage{Rules: []client.RoutingRule{f.rule}}, nil
}

func (f *fakeRoutingRules) Get(context.Context, string) (*client.RoutingRule, error) {
	rule := f.rule
	return &rule, nil
}

func (f *fakeRoutingRules) Create(_ context.Context, in client.RoutingRuleCreateInput) (*client.RoutingRule, error) {
	f.created = &in
	f.rule = client.RoutingRule{
		ID:           "rrl_1",
		DisplayName:  in.DisplayName,
		ModelsConfig: storedModelsConfig(in.ModelsConfig),
		CreatedAt:    "2026-01-01T00:00:00Z",
		UpdatedAt:    "2026-01-01T00:00:00Z",
	}
	if in.Priority != nil {
		f.rule.Priority = *in.Priority
	}
	if in.Enabled != nil {
		f.rule.Enabled = *in.Enabled
	}
	if in.ExpressionCEL != nil {
		f.rule.ExpressionCEL = *in.ExpressionCEL
	}
	rule := f.rule
	return &rule, nil
}

func (f *fakeRoutingRules) Update(_ context.Context, in client.RoutingRuleUpdateInput) (*client.RoutingRule, error) {
	f.updated = &in
	if in.DisplayName != nil {
		f.rule.DisplayName = *in.DisplayName
	}
	switch {
	case in.ModelsConfig != nil:
		f.rule.ModelsConfig = storedModelsConfig(in.ModelsConfig)
	case in.ClearModelsConfig:
		f.rule.ModelsConfig = nil
	}
	f.rule.UpdatedAt = "2026-01-02T00:00:00Z"
	rule := f.rule
	return &rule, nil
}

func (f *fakeRoutingRules) Delete(context.Context, string) error { return nil }

// --- plan / state plumbing --------------------------------------------------

func routingRuleSchema(t *testing.T) schema.Schema {
	t.Helper()
	var sch resource.SchemaResponse
	NewRoutingRuleResource().Schema(context.Background(), resource.SchemaRequest{}, &sch)
	if sch.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", sch.Diagnostics)
	}
	return sch.Schema
}

func routingRuleRaw(t *testing.T, s schema.Schema, m routingRuleResourceModel) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := state.Set(ctx, &m); diags.HasError() {
		t.Fatalf("building value: %v", diags)
	}
	return state.Raw
}

// routingRuleCreate runs the real Create against the fake API and returns the
// state the framework would persist.
func routingRuleCreate(t *testing.T, s schema.Schema, api client.RoutingRulesAPI, plan tftypes.Value) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
	(&routingRuleResource{rules: api}).Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	return resp.State.Raw
}

func routingRuleUpdate(t *testing.T, s schema.Schema, api client.RoutingRulesAPI, plan, prior tftypes.Value) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: prior}}
	(&routingRuleResource{rules: api}).Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: prior},
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Update: %v", resp.Diagnostics)
	}
	return resp.State.Raw
}

// assertMatches is the apply-time contract Terraform enforces: every KNOWN,
// non-null leaf of `want` must survive into `got` unchanged, and a known
// collection must keep its length. Driving it with the PLAN is AssertPlanValid
// ("Provider produced inconsistent result after apply"); driving it with the
// CONFIG proves the next plan is empty, because every attribute the config
// leaves null is Computed and adopts the prior state.
func assertMatches(t *testing.T, label string, want, got tftypes.Value) {
	t.Helper()
	err := tftypes.Walk(want, func(path *tftypes.AttributePath, wantVal tftypes.Value) (bool, error) {
		if !wantVal.IsKnown() || wantVal.IsNull() {
			return false, nil // unknown may resolve to anything; null has nothing below it
		}
		gotIface, _, err := tftypes.WalkAttributePath(got, path)
		if err != nil {
			t.Errorf("%s: %s is missing from the result: %v", label, path, err)
			return false, nil
		}
		gotVal := gotIface.(tftypes.Value)
		switch {
		case wantVal.Type().Is(tftypes.List{}) || wantVal.Type().Is(tftypes.Set{}):
			var wantElems, gotElems []tftypes.Value
			_ = wantVal.As(&wantElems)
			if gotVal.IsNull() || !gotVal.IsKnown() {
				t.Errorf("%s: %s must stay a known list", label, path)
				return false, nil
			}
			_ = gotVal.As(&gotElems)
			if len(wantElems) != len(gotElems) {
				t.Errorf("%s: %s length = %d, want %d", label, path, len(gotElems), len(wantElems))
				return false, nil
			}
		case wantVal.Type().Is(tftypes.Object{}) || wantVal.Type().Is(tftypes.Map{}):
			// Descend: the leaves carry the contract.
		default:
			if !wantVal.Equal(gotVal) {
				t.Errorf("%s: %s = %s, want %s", label, path, gotVal, wantVal)
			}
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", label, err)
	}
}

func assertNoUnknownValues(t *testing.T, v tftypes.Value) {
	t.Helper()
	err := tftypes.Walk(v, func(path *tftypes.AttributePath, val tftypes.Value) (bool, error) {
		if !val.IsKnown() {
			t.Errorf("%s is still unknown after apply", path)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("walking state: %v", err)
	}
}

// routingRuleConfig is the config a user wrote: everything they did not set is
// null. routingRulePlan is what the framework plans from it — every Computed
// attribute with a null config becomes unknown (server_planresourcechange.go's
// MarkComputedNilsAsUnknown), except `id`/`created_at`, which UseStateForUnknown
// pins to the prior state on an update.
func routingRuleConfig(mc *routingRuleModelsConfigModel) routingRuleResourceModel {
	return routingRuleResourceModel{
		ID:           types.StringNull(),
		DisplayName:  types.StringValue("route"),
		Description:  types.StringNull(),
		Enabled:      types.BoolNull(),
		ProjectID:    types.StringNull(),
		Priority:     types.Int64Null(),
		ModelsConfig: mc,
		CreatedAt:    types.StringNull(),
		UpdatedAt:    types.StringNull(),
	}
}

func routingRulePlan(mc *routingRuleModelsConfigModel) routingRuleResourceModel {
	m := routingRuleConfig(mc)
	m.ID = types.StringUnknown()
	m.Description = types.StringUnknown()
	m.Enabled = types.BoolUnknown()
	m.Priority = types.Int64Unknown()
	m.CreatedAt = types.StringUnknown()
	m.UpdatedAt = types.StringUnknown()
	if m.ModelsConfig != nil {
		planned := *m.ModelsConfig
		planned.Models = append([]routingRuleModelRefModel(nil), planned.Models...)
		for i, e := range planned.Models {
			if e.DisplayName.IsNull() {
				planned.Models[i].DisplayName = types.StringUnknown()
			}
			if e.Weight.IsNull() {
				planned.Models[i].Weight = types.Float64Unknown()
			}
			if e.IntegrationID.IsNull() {
				planned.Models[i].IntegrationID = types.StringUnknown()
			}
		}
		m.ModelsConfig = &planned
	}
	return m
}

// minimalModelsConfig is the smallest config the schema accepts: a mode and one
// model reference, with every server-defaulted leaf left out.
func minimalModelsConfig() *routingRuleModelsConfigModel {
	return &routingRuleModelsConfigModel{
		Mode: types.StringValue("fallback"),
		Models: []routingRuleModelRefModel{{
			Model:         types.StringValue("openai/gpt-4o"),
			DisplayName:   types.StringNull(),
			Weight:        types.Float64Null(),
			IntegrationID: types.StringNull(),
		}},
	}
}

// --- the four QA scenarios --------------------------------------------------

// A config with no models_config must not send the field at all (the server
// rejects an explicit null) and must read back as an absent block.
func TestRoutingRuleCreateWithoutModelsConfig(t *testing.T) {
	s := routingRuleSchema(t)
	api := &fakeRoutingRules{}

	config := routingRuleRaw(t, s, routingRuleConfig(nil))
	plan := routingRuleRaw(t, s, routingRulePlan(nil))
	state := routingRuleCreate(t, s, api, plan)

	if api.created.ModelsConfig != nil {
		t.Errorf("an omitted block must not be written, got %+v", api.created.ModelsConfig)
	}
	assertNoUnknownValues(t, state)
	assertMatches(t, "plan", plan, state)
	assertMatches(t, "config", config, state)
}

// A model entry carrying only `model` must survive the server's defaulting
// (display_name "", weight 0.5, integration_id "") without a tainted resource:
// the three defaulted leaves are Optional+Computed, so their planned unknown
// absorbs whatever the server chose.
func TestRoutingRuleCreateWithMinimalModelsConfig(t *testing.T) {
	s := routingRuleSchema(t)
	api := &fakeRoutingRules{}

	config := routingRuleRaw(t, s, routingRuleConfig(minimalModelsConfig()))
	plan := routingRuleRaw(t, s, routingRulePlan(minimalModelsConfig()))
	state := routingRuleCreate(t, s, api, plan)

	sent := api.created.ModelsConfig
	if sent == nil || len(sent.Models) != 1 {
		t.Fatalf("models_config not written: %+v", sent)
	}
	if sent.Models[0].Weight != nil || sent.Models[0].DisplayName != "" || sent.Models[0].IntegrationID != "" {
		t.Errorf("unset leaves must be written as absent, got %+v", sent.Models[0])
	}
	assertNoUnknownValues(t, state)
	assertMatches(t, "plan", plan, state)
	assertMatches(t, "config", config, state)

	// The server's defaults landed in state, so a re-apply is a no-op.
	var got routingRuleResourceModel
	if diags := (tfsdk.State{Schema: s, Raw: state}).Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("reading state: %v", diags)
	}
	entry := got.ModelsConfig.Models[0]
	if entry.Weight.ValueFloat64() != 0.5 {
		t.Errorf("weight = %v, want the server default 0.5", entry.Weight)
	}
	if entry.DisplayName.ValueString() != "" || entry.IntegrationID.ValueString() != "" {
		t.Errorf("server-defaulted leaves not stored: %+v", entry)
	}
}

// Adding the block to a rule that had none writes it; removing it again must
// CLEAR it server-side, which only clear_models_config can do.
func TestRoutingRuleUpdateAddsAndRemovesModelsConfig(t *testing.T) {
	s := routingRuleSchema(t)
	api := &fakeRoutingRules{}

	prior := routingRuleCreate(t, s, api, routingRuleRaw(t, s, routingRulePlan(nil)))

	// Add the block. id/created_at come from prior state (UseStateForUnknown).
	addModel := routingRulePlan(minimalModelsConfig())
	addModel.ID = types.StringValue("rrl_1")
	addModel.CreatedAt = types.StringValue("2026-01-01T00:00:00Z")
	addPlan := routingRuleRaw(t, s, addModel)
	added := routingRuleUpdate(t, s, api, addPlan, prior)

	if api.updated.ModelsConfig == nil || api.updated.ClearModelsConfig {
		t.Fatalf("adding a block must write it and never clear: %+v", api.updated)
	}
	assertNoUnknownValues(t, added)
	assertMatches(t, "plan", addPlan, added)
	assertMatches(t, "config", routingRuleRaw(t, s, routingRuleConfig(minimalModelsConfig())), added)

	// Remove it again.
	removeModel := routingRulePlan(nil)
	removeModel.ID = types.StringValue("rrl_1")
	removeModel.CreatedAt = types.StringValue("2026-01-01T00:00:00Z")
	removePlan := routingRuleRaw(t, s, removeModel)
	removed := routingRuleUpdate(t, s, api, removePlan, added)

	if !api.updated.ClearModelsConfig || api.updated.ModelsConfig != nil {
		t.Fatalf("removing the block must request a clear: %+v", api.updated)
	}
	if api.rule.ModelsConfig != nil {
		t.Error("the stored models_config was not cleared")
	}
	assertNoUnknownValues(t, removed)
	assertMatches(t, "plan", removePlan, removed)
	assertMatches(t, "config", routingRuleRaw(t, s, routingRuleConfig(nil)), removed)
}

// A refresh of an unchanged rule must be a fixed point: the read-back state has
// to equal the state the apply produced, or every plan shows a diff.
func TestRoutingRuleReadIsAFixedPoint(t *testing.T) {
	s := routingRuleSchema(t)
	ctx := context.Background()
	api := &fakeRoutingRules{}

	state := routingRuleCreate(t, s, api, routingRuleRaw(t, s, routingRulePlan(minimalModelsConfig())))

	resp := resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: state}}
	(&routingRuleResource{rules: api}).Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: state}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read: %v", resp.Diagnostics)
	}
	if !state.Equal(resp.State.Raw) {
		t.Errorf("refresh changed state:\n apply:   %s\n refresh: %s", state, resp.State.Raw)
	}
}

// --- schema decisions -------------------------------------------------------

func TestRoutingRuleModelsConfigSchema(t *testing.T) {
	s := routingRuleSchema(t)

	mc, ok := s.Attributes["models_config"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatal("models_config must be a SingleNestedAttribute, not an opaque JSON string")
	}
	if !mc.Optional {
		t.Error("models_config must be Optional")
	}
	if mc.Computed {
		t.Error("models_config must NOT be Computed — removing it from config must clear it, not adopt the prior value")
	}
	if mode := mc.Attributes["mode"].(schema.StringAttribute); !mode.Required || len(mode.Validators) == 0 {
		t.Error("mode must be Required with an enum validator (the server rejects any other value)")
	}
	models, ok := mc.Attributes["models"].(schema.ListNestedAttribute)
	if !ok || !models.Required || len(models.Validators) == 0 {
		t.Fatal("models must be a Required ListNestedAttribute with a non-empty validator")
	}
	if model := models.NestedObject.Attributes["model"].(schema.StringAttribute); !model.Required {
		t.Error("models[].model must be Required")
	}
	// The three leaves the server fills in must be Optional+Computed, or the
	// server's defaults produce an inconsistent result after apply.
	for _, name := range []string{"display_name", "integration_id"} {
		attr := models.NestedObject.Attributes[name].(schema.StringAttribute)
		if !attr.Optional || !attr.Computed {
			t.Errorf("models[].%s must be Optional+Computed", name)
		}
	}
	weight := models.NestedObject.Attributes["weight"].(schema.Float64Attribute)
	if !weight.Optional || !weight.Computed {
		t.Error("models[].weight must be Optional+Computed")
	}
	if len(weight.Validators) == 0 {
		t.Error("models[].weight needs a range validator")
	}
}

// --- apply-time weight guard ------------------------------------------------

// A weight that is only known at apply (an interpolated value) skips the schema
// validator, and a zero would come back from the server as 0.5 — an inconsistent
// result on an already-created rule. The pre-flight rejects it before any call.
func TestRoutingRuleZeroWeightRejectedBeforeTheWrite(t *testing.T) {
	ctx := context.Background()
	s := routingRuleSchema(t)

	zeroWeight := minimalModelsConfig()
	zeroWeight.Models[0].Weight = types.Float64Value(0)
	plan := routingRuleRaw(t, s, routingRulePlan(zeroWeight))

	api := &fakeRoutingRules{}
	createResp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
	(&routingRuleResource{rules: api}).Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan}}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("a zero weight must fail the create")
	}
	if api.created != nil {
		t.Error("the rule must not be created before the weight is rejected")
	}
	if detail := createResp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, "greater than 0") {
		t.Errorf("the diagnostic must explain the constraint, got %q", detail)
	}

	// Same guard on update, so an interpolation cannot poison an existing rule.
	prior := routingRuleCreate(t, s, api, routingRuleRaw(t, s, routingRulePlan(minimalModelsConfig())))
	api.updated = nil
	updateResp := resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: prior}}
	(&routingRuleResource{rules: api}).Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: prior},
	}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Fatal("a zero weight must fail the update")
	}
	if api.updated != nil {
		t.Error("the rule must not be written before the weight is rejected")
	}
}
