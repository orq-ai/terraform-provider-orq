package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

const testProfileARN = "arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123"

// TestBedrockApplyKeepsWriteOnlyAssumeRole proves apply() never clobbers the
// assume-role pair: the server strips both from every response, so `data` (the
// plan on create/update, prior state on read) is the only source for them.
func TestBedrockApplyKeepsWriteOnlyAssumeRole(t *testing.T) {
	r := &bedrockModelResource{}
	m := &bedrockModelResourceModel{
		AssumeRoleArn:        types.StringValue("arn:aws:iam::123456789012:role/bedrock"),
		AssumeRoleExternalID: types.StringValue("ext-1"),
		Region:               types.StringValue("eu-central-1"),
		ModelDeveloper:       types.StringValue("anthropic"),
	}
	r.apply(&client.BedrockModel{
		ID:                  "mdl_1",
		DisplayName:         "tf bedrock",
		ModelID:             testProfileARN,
		ModelType:           "chat",
		Provider:            client.ModelProviderAWS,
		Region:              "eu-central-1",
		ModelDeveloper:      "anthropic",
		ModelFamily:         "claude",
		AuthMode:            client.BedrockAuthModePodIdentity,
		InferenceProfileArn: testProfileARN,
		Created:             "2020-01-01T00:00:00Z",
		Updated:             "2020-01-02T00:00:00Z",
	}, m, false)

	if m.AssumeRoleArn.ValueString() != "arn:aws:iam::123456789012:role/bedrock" {
		t.Errorf("assume_role_arn must be preserved, got %q", m.AssumeRoleArn.ValueString())
	}
	if m.AssumeRoleExternalID.ValueString() != "ext-1" {
		t.Errorf("assume_role_external_id must be preserved, got %q", m.AssumeRoleExternalID.ValueString())
	}
	if m.ID.ValueString() != "mdl_1" || m.ModelID.ValueString() != testProfileARN {
		t.Errorf("identity not refreshed: %+v", m)
	}
	if m.ModelFamily.ValueString() != "claude" || m.AuthMode.ValueString() != client.BedrockAuthModePodIdentity {
		t.Errorf("model_family/auth_mode not refreshed: %+v", m)
	}
	if !m.IntegrationID.IsNull() {
		t.Errorf("integration_id must read back null for a pod-identity model, got %v", m.IntegrationID)
	}
}

// TestBedrockApplyToolCallingUsesHasFunctions proves supports_tool_calling
// refreshes in BOTH directions: the metadata field is dropped when false, but the
// server also mirrors it into the always-present top-level has_functions.
func TestBedrockApplyToolCallingUsesHasFunctions(t *testing.T) {
	r := &bedrockModelResource{}

	t.Run("metadata present wins", func(t *testing.T) {
		m := &bedrockModelResourceModel{SupportsToolCalling: types.BoolValue(false)}
		r.apply(&client.BedrockModel{ID: "m", SupportsToolCalling: bptr(true), HasFunctions: true}, m, false)
		if !m.SupportsToolCalling.ValueBool() {
			t.Error("supports_tool_calling must refresh from metadata")
		}
	})

	t.Run("metadata absent falls back to has_functions false", func(t *testing.T) {
		m := &bedrockModelResourceModel{SupportsToolCalling: types.BoolValue(true)}
		r.apply(&client.BedrockModel{ID: "m", HasFunctions: false}, m, false)
		if m.SupportsToolCalling.ValueBool() {
			t.Error("a flip to false must surface via has_functions, not be retained")
		}
		if m.SupportsToolCalling.IsNull() {
			t.Error("supports_tool_calling must stay concrete (has_functions is always serialized)")
		}
	})
}

// TestBedrockApplyReadIsAuthoritative locks in the read semantics: the response
// is the COMPLETE model document, so an absent `omitempty` capability boolean
// means false and an absent parameter slider means unset. Retaining the prior
// value instead would hide every out-of-band true→false / cleared change.
func TestBedrockApplyReadIsAuthoritative(t *testing.T) {
	r := &bedrockModelResource{}
	m := &bedrockModelResourceModel{
		InputCost:                 types.Float64Value(5),
		OutputCost:                types.Float64Value(10),
		SupportsVision:            types.BoolValue(true),
		SupportsStrictTool:        types.BoolValue(true),
		SupportsJSONMode:          types.BoolValue(true),
		SupportsJSONSchema:        types.BoolValue(true),
		HasReasoning:              types.BoolValue(true),
		SupportsAdaptiveReasoning: types.BoolValue(true),
		SupportsExtendedThinking:  types.BoolValue(true),
		SupportsToolCalling:       types.BoolValue(true),
		MaxTokens:                 types.Int64Value(4096),
		Temperature:               types.Float64Value(0.7),
	}
	// Server dropped both costs to 0, omitted every metadata capability field and
	// carries no parameter sliders at all.
	r.apply(&client.BedrockModel{ID: "m", InputCost: f64ptr(0), OutputCost: f64ptr(0)}, m, false)

	if m.InputCost.ValueFloat64() != 0 || m.OutputCost.ValueFloat64() != 0 {
		t.Errorf("costs must refresh to 0 on read: in=%v out=%v", m.InputCost, m.OutputCost)
	}
	for name, v := range map[string]types.Bool{
		"supports_vision":             m.SupportsVision,
		"supports_strict_tool":        m.SupportsStrictTool,
		"supports_json_mode":          m.SupportsJSONMode,
		"supports_json_schema":        m.SupportsJSONSchema,
		"has_reasoning":               m.HasReasoning,
		"supports_adaptive_reasoning": m.SupportsAdaptiveReasoning,
		"supports_extended_thinking":  m.SupportsExtendedThinking,
		"supports_tool_calling":       m.SupportsToolCalling,
	} {
		if v.ValueBool() {
			t.Errorf("%s must refresh to false on server absence (an out-of-band flip must surface)", name)
		}
		if v.IsNull() {
			t.Errorf("%s must be a concrete false, not null", name)
		}
	}
	if !m.MaxTokens.IsNull() || !m.Temperature.IsNull() {
		t.Errorf("a cleared parameter slider must refresh to null: %v %v", m.MaxTokens, m.Temperature)
	}
}

// TestBedrockApplyRefreshesParameters proves max_tokens / temperature ARE
// readable — the server stores them as sliders in the parameter list — so an
// out-of-band change surfaces as drift and import recovers them.
func TestBedrockApplyRefreshesParameters(t *testing.T) {
	r := &bedrockModelResource{}
	m := &bedrockModelResourceModel{
		MaxTokens:   types.Int64Value(4096),
		Temperature: types.Float64Value(0.7),
	}
	r.apply(&client.BedrockModel{ID: "m", MaxTokens: i64ptr(8192), Temperature: f64ptr(0.2)}, m, false)
	if m.MaxTokens.ValueInt64() != 8192 {
		t.Errorf("max_tokens must refresh from the parameter list, got %v", m.MaxTokens)
	}
	if m.Temperature.ValueFloat64() != 0.2 {
		t.Errorf("temperature must refresh from the parameter list, got %v", m.Temperature)
	}

	// Import: state starts empty and the read must populate both.
	imported := &bedrockModelResourceModel{MaxTokens: types.Int64Null(), Temperature: types.Float64Null()}
	r.apply(&client.BedrockModel{ID: "m", MaxTokens: i64ptr(1024), Temperature: f64ptr(0)}, imported, false)
	if imported.MaxTokens.ValueInt64() != 1024 {
		t.Errorf("import must recover max_tokens, got %v", imported.MaxTokens)
	}
	if imported.Temperature.IsNull() || imported.Temperature.ValueFloat64() != 0 {
		t.Errorf("import must recover a temperature of 0 (the server emits a slider for it), got %v", imported.Temperature)
	}
}

// TestBedrockApplyPreservesPlannedNumerics proves the create/update path keeps a
// KNOWN planned numeric (post-apply state must equal the plan) and fills a
// null/unknown one from the server.
func TestBedrockApplyPreservesPlannedNumerics(t *testing.T) {
	r := &bedrockModelResource{}

	m := &bedrockModelResourceModel{
		InputCost:   types.Float64Value(0.003),
		OutputCost:  types.Float64Unknown(),
		MaxTokens:   types.Int64Value(4096),
		Temperature: types.Float64Value(0.7),
	}
	r.apply(&client.BedrockModel{
		ID: "m", InputCost: f64ptr(0), OutputCost: f64ptr(0.015),
		MaxTokens: i64ptr(999), Temperature: f64ptr(0.1),
	}, m, true)
	if m.InputCost.ValueFloat64() != 0.003 {
		t.Errorf("known planned input_cost must be preserved, got %v", m.InputCost)
	}
	if m.OutputCost.ValueFloat64() != 0.015 {
		t.Errorf("unknown planned output_cost must be filled from the server, got %v", m.OutputCost)
	}
	if m.MaxTokens.ValueInt64() != 4096 || m.Temperature.ValueFloat64() != 0.7 {
		t.Errorf("known planned parameters must be preserved: %v %v", m.MaxTokens, m.Temperature)
	}

	// A null plan means "cleared": the server value must NOT be resurrected.
	cleared := &bedrockModelResourceModel{MaxTokens: types.Int64Null(), Temperature: types.Float64Null()}
	r.apply(&client.BedrockModel{ID: "m", MaxTokens: i64ptr(4096), Temperature: f64ptr(0.7)}, cleared, true)
	if !cleared.MaxTokens.IsNull() || !cleared.Temperature.IsNull() {
		t.Errorf("a cleared parameter must stay null on the write path: %v %v", cleared.MaxTokens, cleared.Temperature)
	}
}

// TestBedrockApplyResolvesUnknowns proves a create-time unknown never survives
// into post-apply state: an Optional+Computed string collapses to null when the
// server omits it, and an Optional+Computed capability boolean resolves to the
// server's authoritative false.
func TestBedrockApplyResolvesUnknowns(t *testing.T) {
	r := &bedrockModelResource{}
	m := &bedrockModelResourceModel{
		ModelFamily:    types.StringUnknown(),
		HasReasoning:   types.BoolUnknown(),
		SupportsVision: types.BoolUnknown(),
	}
	r.apply(&client.BedrockModel{ID: "m"}, m, true)
	if !m.ModelFamily.IsNull() {
		t.Errorf("model_family must collapse unknown → null when the server omits it, got %v", m.ModelFamily)
	}
	for name, v := range map[string]types.Bool{
		"has_reasoning":   m.HasReasoning,
		"supports_vision": m.SupportsVision,
	} {
		if v.IsUnknown() || v.IsNull() || v.ValueBool() {
			t.Errorf("%s must resolve to a concrete false, got %v", name, v)
		}
	}
}

// TestBedrockParametersEmpty mirrors the server's buildBedrockParameters: it
// decides whether a plan/state would produce an empty parameter list, which is
// the request shape the update endpoint silently ignores.
func TestBedrockParametersEmpty(t *testing.T) {
	cases := []struct {
		name string
		m    bedrockModelResourceModel
		want bool
	}{
		{"nothing set", bedrockModelResourceModel{
			MaxTokens: types.Int64Null(), Temperature: types.Float64Null(), HasReasoning: types.BoolValue(false)}, true},
		{"has_reasoning null counts as unset", bedrockModelResourceModel{
			MaxTokens: types.Int64Null(), Temperature: types.Float64Null(), HasReasoning: types.BoolNull()}, true},
		{"temperature 0 still emits a slider", bedrockModelResourceModel{
			MaxTokens: types.Int64Null(), Temperature: types.Float64Value(0), HasReasoning: types.BoolValue(false)}, false},
		{"max_tokens set", bedrockModelResourceModel{
			MaxTokens: types.Int64Value(4096), Temperature: types.Float64Null(), HasReasoning: types.BoolValue(false)}, false},
		{"has_reasoning alone", bedrockModelResourceModel{
			MaxTokens: types.Int64Null(), Temperature: types.Float64Null(), HasReasoning: types.BoolValue(true)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bedrockParametersEmpty(&tc.m); got != tc.want {
				t.Errorf("bedrockParametersEmpty() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestValidateBedrockAuthMode locks in the credential-resolution invariants.
func TestValidateBedrockAuthMode(t *testing.T) {
	integration := types.StringValue(client.BedrockAuthModeIntegration)
	podIdentity := types.StringValue(client.BedrockAuthModePodIdentity)
	set := types.StringValue("x")
	null := types.StringNull()

	cases := []struct {
		name      string
		cfg       bedrockAuthConfig
		wantError bool
	}{
		{"integration with integration_id ok", bedrockAuthConfig{AuthMode: integration, IntegrationID: set}, false},
		{"integration without integration_id rejected", bedrockAuthConfig{AuthMode: integration, IntegrationID: null}, true},
		{"integration with assume_role rejected", bedrockAuthConfig{AuthMode: integration, IntegrationID: set, AssumeRoleArn: set}, true},
		{"pod-identity alone ok", bedrockAuthConfig{AuthMode: podIdentity}, false},
		{"pod-identity with assume_role ok", bedrockAuthConfig{AuthMode: podIdentity, AssumeRoleArn: set, AssumeRoleExternalID: set}, false},
		{"pod-identity with integration_id rejected", bedrockAuthConfig{AuthMode: podIdentity, IntegrationID: set}, true},
		{"external id without role arn rejected", bedrockAuthConfig{AuthMode: podIdentity, AssumeRoleExternalID: set}, true},
		// Unknowns defer to the Create/Update recheck, which runs once the
		// interpolated values are known (the framework never re-runs ValidateConfig).
		{"unknown auth_mode defers", bedrockAuthConfig{AuthMode: types.StringUnknown(), IntegrationID: set}, false},
		{"integration with unknown integration_id defers", bedrockAuthConfig{AuthMode: integration, IntegrationID: types.StringUnknown()}, false},
		{"unknown role arn with external id defers", bedrockAuthConfig{AuthMode: podIdentity, AssumeRoleArn: types.StringUnknown(), AssumeRoleExternalID: set}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := validateBedrockAuthMode(tc.cfg)
			if diags.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v (%v)", diags.HasError(), tc.wantError, diags)
			}
		})
	}
}

// bedrockRaw builds a resource object with every attribute null except the given
// overrides, so a plan/state can be assembled without spelling out ~25 fields.
func bedrockRaw(ctx context.Context, s schema.Schema, override map[string]tftypes.Value) tftypes.Value {
	objType := s.Type().TerraformType(ctx).(tftypes.Object)
	vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, ty := range objType.AttributeTypes {
		if v, ok := override[name]; ok {
			vals[name] = v
			continue
		}
		vals[name] = tftypes.NewValue(ty, nil)
	}
	return tftypes.NewValue(objType, vals)
}

// TestBedrockModifyPlanAllParametersCleared proves the cross-field guard: the
// update endpoint writes its rebuilt parameter list only when non-empty, so
// clearing the LAST of max_tokens / temperature / has_reasoning cannot be applied
// in place and must force replacement instead of leaving a stale slider that
// re-appears on every refresh.
func TestBedrockModifyPlanAllParametersCleared(t *testing.T) {
	ctx := context.Background()
	r := NewBedrockModelResource().(*bedrockModelResource)
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	s := sch.Schema

	maxTokensSet := map[string]tftypes.Value{"max_tokens": tftypes.NewValue(tftypes.Number, 4096)}
	temperatureSet := map[string]tftypes.Value{"temperature": tftypes.NewValue(tftypes.Number, 0.7)}

	run := func(stateOverride, planOverride map[string]tftypes.Value) []string {
		resp := &resource.ModifyPlanResponse{}
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{
			State: tfsdk.State{Schema: s, Raw: bedrockRaw(ctx, s, stateOverride)},
			Plan:  tfsdk.Plan{Schema: s, Raw: bedrockRaw(ctx, s, planOverride)},
		}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("ModifyPlan diagnostics: %v", resp.Diagnostics)
		}
		out := make([]string, 0, len(resp.RequiresReplace))
		for _, p := range resp.RequiresReplace {
			out = append(out, p.String())
		}
		return out
	}

	if got := run(maxTokensSet, nil); len(got) == 0 {
		t.Error("clearing the last parameter must force replacement")
	} else {
		for _, want := range []string{"max_tokens", "temperature", "has_reasoning"} {
			if !strings.Contains(strings.Join(got, ","), want) {
				t.Errorf("replacement should be attributed to %s, got %v", want, got)
			}
		}
	}
	if got := run(maxTokensSet, temperatureSet); len(got) != 0 {
		t.Errorf("clearing one parameter while another remains is applied in place, got %v", got)
	}
	if got := run(nil, maxTokensSet); len(got) != 0 {
		t.Errorf("adding a parameter must not force replacement, got %v", got)
	}
	if got := run(maxTokensSet, maxTokensSet); len(got) != 0 {
		t.Errorf("an unchanged parameter set must not force replacement, got %v", got)
	}

	// Create (null prior state) and destroy (null plan) are skipped outright.
	nullRaw := tftypes.NewValue(s.Type().TerraformType(ctx), nil)
	for _, tc := range []struct {
		name  string
		state tftypes.Value
		plan  tftypes.Value
	}{
		{"create", nullRaw, bedrockRaw(ctx, s, nil)},
		{"destroy", bedrockRaw(ctx, s, maxTokensSet), nullRaw},
	} {
		resp := &resource.ModifyPlanResponse{}
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{
			State: tfsdk.State{Schema: s, Raw: tc.state},
			Plan:  tfsdk.Plan{Schema: s, Raw: tc.plan},
		}, resp)
		if len(resp.RequiresReplace) != 0 || resp.Diagnostics.HasError() {
			t.Errorf("%s must be skipped by ModifyPlan: %v %v", tc.name, resp.RequiresReplace, resp.Diagnostics)
		}
	}
}

// TestBedrockARNValidator proves model_id accepts only an inference-profile ARN,
// matching the regex the server applies on create AND update.
func TestBedrockARNValidator(t *testing.T) {
	ctx := context.Background()
	check := func(s string) bool {
		req := validator.StringRequest{Path: path.Root("model_id"), ConfigValue: types.StringValue(s)}
		resp := &validator.StringResponse{}
		bedrockARNValidator{}.ValidateString(ctx, req, resp)
		return resp.Diagnostics.HasError()
	}
	accept := []string{
		testProfileARN,
		"arn:aws:bedrock:us-east-1:1:inference-profile/us.anthropic.claude-sonnet-4-v1",
	}
	for _, s := range accept {
		if check(s) {
			t.Errorf("%q must be accepted", s)
		}
	}
	reject := []string{
		"anthropic.claude-sonnet-4-20250514-v1:0",                                  // bare foundation-model id
		"arn:aws:bedrock:eu-central-1:123456789012:foundation-model/claude",        // wrong resource type
		"arn:aws:bedrock:eu-central-1:notanaccount:inference-profile/x",            // non-numeric account
		"arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/", // empty profile id
		"",
	}
	for _, s := range reject {
		if !check(s) {
			t.Errorf("%q must be rejected", s)
		}
	}
	// Null / unknown is skipped.
	resp := &validator.StringResponse{}
	bedrockARNValidator{}.ValidateString(ctx,
		validator.StringRequest{Path: path.Root("model_id"), ConfigValue: types.StringNull()}, resp)
	if resp.Diagnostics.HasError() {
		t.Error("null model_id must be skipped by the validator")
	}
}

// TestBedrockModelNotManaged proves the guard that keeps Read/ImportState from
// PATCHing or DELETEing a model this resource does not own.
func TestBedrockModelNotManaged(t *testing.T) {
	bedrock := &client.BedrockModel{
		ID: "m", Provider: client.ModelProviderAWS, Owner: "ws_1",
		InferenceProfileArn: testProfileARN, AuthMode: client.BedrockAuthModePodIdentity,
	}
	if _, _, notManaged := bedrockModelNotManaged(bedrock); notManaged {
		t.Error("a bedrock model must be accepted")
	}
	for name, m := range map[string]*client.BedrockModel{
		"system model":       {ID: "openai/gpt-4o", Provider: "openai", Owner: "system"},
		"openai-like custom": {ID: "m", Provider: client.ModelProviderOpenAILike, Owner: "ws_1"},
		"legacy aws model":   {ID: "m", Provider: client.ModelProviderAWS, Owner: "ws_1"},
	} {
		summary, detail, notManaged := bedrockModelNotManaged(m)
		if !notManaged {
			t.Errorf("%s must be refused", name)
		}
		if summary == "" || detail == "" {
			t.Errorf("%s refusal must carry a diagnostic", name)
		}
	}
}

// TestBedrockSchemaReplaceSet locks in which attributes force replacement: the
// three the PATCH endpoint does not accept, plus nothing that IS patchable.
func TestBedrockSchemaReplaceSet(t *testing.T) {
	ctx := context.Background()
	var sch resource.SchemaResponse
	NewBedrockModelResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	if sch.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", sch.Diagnostics)
	}

	isReplace := func(pm planmodifier.String) bool {
		d := strings.ToLower(pm.Description(ctx))
		return strings.Contains(d, "destroy") && strings.Contains(d, "recreate")
	}
	hasReplace := func(name string) bool {
		attr, ok := sch.Schema.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a StringAttribute", name)
		}
		for _, pm := range attr.PlanModifiers {
			if isReplace(pm) {
				return true
			}
		}
		return false
	}

	for _, name := range []string{"auth_mode", "integration_id", "model_type"} {
		if !hasReplace(name) {
			t.Errorf("%s is not accepted by the PATCH endpoint and must force replacement", name)
		}
	}
	// model_id (the ARN), region, model_developer and display_name ARE patchable.
	for _, name := range []string{"model_id", "region", "model_developer", "display_name", "model_family", "description"} {
		if hasReplace(name) {
			t.Errorf("%s is patchable and must be mutable in place", name)
		}
	}

	externalID, ok := sch.Schema.Attributes["assume_role_external_id"].(schema.StringAttribute)
	if !ok {
		t.Fatal("assume_role_external_id is not a StringAttribute")
	}
	if !externalID.Sensitive {
		t.Error("assume_role_external_id must be Sensitive (the server treats it as a credential)")
	}
	// The assume-role pair is never echoed, so neither may be Computed: a Computed
	// unknown could never be resolved from a read.
	for _, name := range []string{"assume_role_arn", "assume_role_external_id"} {
		attr := sch.Schema.Attributes[name].(schema.StringAttribute)
		if attr.Computed {
			t.Errorf("%s is write-only and must not be Computed", name)
		}
	}

	// max_tokens / temperature ARE readable (parameter sliders), so they must be
	// Optional-only: Computed would make a null config mean "keep the prior value"
	// and the removal could never be applied, and an unknown plan value would be
	// dropped from the PATCH, deleting its sibling from the server's rebuilt list.
	if sch.Schema.Attributes["max_tokens"].(schema.Int64Attribute).Computed {
		t.Error("max_tokens is refreshable and must be Optional-only so removing it clears it")
	}
	if sch.Schema.Attributes["temperature"].(schema.Float64Attribute).Computed {
		t.Error("temperature is refreshable and must be Optional-only so removing it clears it")
	}
}

// TestRequiresReplaceOnClear proves the PATCH-cannot-clear handling: removing a
// previously set assume-role value forces replacement (an in-place update would
// silently leave the old value attached), while setting, changing or leaving it
// unset does not.
func TestRequiresReplaceOnClear(t *testing.T) {
	ctx := context.Background()
	pm := requiresReplaceOnClear("assume_role_arn")

	// A non-null raw plan/state keeps the framework from short-circuiting the
	// create (null state) and destroy (null plan) cases.
	nonNullRaw := tftypes.NewValue(tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{"assume_role_arn": tftypes.String},
	}, map[string]tftypes.Value{"assume_role_arn": tftypes.NewValue(tftypes.String, "x")})

	run := func(state, plan types.String) bool {
		req := planmodifier.StringRequest{
			Path:       path.Root("assume_role_arn"),
			StateValue: state,
			PlanValue:  plan,
			State:      tfsdk.State{Raw: nonNullRaw},
			Plan:       tfsdk.Plan{Raw: nonNullRaw},
		}
		resp := &planmodifier.StringResponse{PlanValue: plan}
		pm.PlanModifyString(ctx, req, resp)
		return resp.RequiresReplace
	}

	set := types.StringValue("arn:aws:iam::1:role/a")
	other := types.StringValue("arn:aws:iam::1:role/b")
	null := types.StringNull()

	if !run(set, null) {
		t.Error("clearing a set assume_role_arn must force replacement")
	}
	if run(set, other) {
		t.Error("changing assume_role_arn is patchable and must not force replacement")
	}
	if run(null, set) {
		t.Error("setting assume_role_arn for the first time must not force replacement")
	}
	if run(null, null) {
		t.Error("leaving assume_role_arn unset must not force replacement")
	}
}

// TestBedrockModelTypeEnum proves model_type accepts only the two values the
// server's Bedrock create endpoint validates.
func TestBedrockModelTypeEnum(t *testing.T) {
	ctx := context.Background()
	var sch resource.SchemaResponse
	NewBedrockModelResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	attr := sch.Schema.Attributes["model_type"].(schema.StringAttribute)
	rejected := func(v string) bool {
		req := validator.StringRequest{Path: path.Root("model_type"), ConfigValue: types.StringValue(v)}
		resp := &validator.StringResponse{}
		for _, val := range attr.Validators {
			val.ValidateString(ctx, req, resp)
		}
		return resp.Diagnostics.HasError()
	}
	for _, v := range []string{"chat", "embedding"} {
		if rejected(v) {
			t.Errorf("%q must be an accepted model_type", v)
		}
	}
	for _, v := range []string{"completion", "image", "rerank", ""} {
		if !rejected(v) {
			t.Errorf("%q must be rejected (the server accepts only chat/embedding)", v)
		}
	}
}

// TestBedrockAuthModeEnum proves auth_mode is constrained to the two modes the
// server validates.
func TestBedrockAuthModeEnum(t *testing.T) {
	ctx := context.Background()
	var sch resource.SchemaResponse
	NewBedrockModelResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	attr := sch.Schema.Attributes["auth_mode"].(schema.StringAttribute)
	if !attr.Required {
		t.Error("auth_mode must be Required")
	}
	rejected := func(v string) bool {
		req := validator.StringRequest{Path: path.Root("auth_mode"), ConfigValue: types.StringValue(v)}
		resp := &validator.StringResponse{}
		for _, val := range attr.Validators {
			val.ValidateString(ctx, req, resp)
		}
		return resp.Diagnostics.HasError()
	}
	for _, v := range []string{client.BedrockAuthModeIntegration, client.BedrockAuthModePodIdentity} {
		if rejected(v) {
			t.Errorf("%q must be an accepted auth_mode", v)
		}
	}
	for _, v := range []string{"pod_identity", "iam", "access-key", ""} {
		if !rejected(v) {
			t.Errorf("%q must be rejected", v)
		}
	}
}

func i64ptr(i int64) *int64 { return &i }
