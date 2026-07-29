package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// TestModelApplyPreservesSecretAndMetadata proves apply() never clobbers the
// secret api_key (never echoed by the server); refreshes input_cost/output_cost
// (always on the wire); and, when the server OMITS a metadata omitempty field
// (nil — indistinguishable from "not applied for this model_type"), keeps the
// operator's configured value (no false drift), while refreshing the reliably
// round-tripped identity fields.
func TestModelApplyPreservesSecretAndMetadata(t *testing.T) {
	r := &modelResource{}
	// `m` stands in for the plan (Create/Update) or prior state (Read): it carries
	// the operator's configured secret + metadata.
	m := &modelResourceModel{
		APIKey:              types.StringValue("sk-secret-xyz"),
		InputCost:           types.Float64Value(0.5),
		OutputCost:          types.Float64Value(1.5),
		MaxTokens:           types.Int64Value(8192),
		Temperature:         types.Float64Value(0.7),
		HasReasoning:        types.BoolValue(true),
		SupportsVision:      types.BoolValue(true),
		SupportsToolCalling: types.BoolValue(true),
		SupportsStrictTool:  types.BoolValue(false),
		SupportsImageEdit:   types.BoolValue(false),
		CostPerImage:        types.Float64Value(0.02),
		// stale identity values that MUST be refreshed from the server projection:
		Region:  types.StringValue("europe"),
		BaseURL: types.StringValue("http://host.docker.internal:1234/v1"),
	}

	// Server projection: identity fields + the always-present input/output cost, but
	// NO api_key and NO metadata omitempty fields (exactly what the real API returns
	// when those capabilities are false/0).
	r.apply(&client.Model{
		ID:          "mdl_uuid_1",
		DisplayName: "tf model",
		ModelID:     "liquid/lfm2.5-1.2b",
		ModelType:   "chat",
		Region:      "europe",
		BaseURL:     "http://host.docker.internal:1234/v1",
		Description: "a custom model",
		InputCost:   f64ptr(0.5),
		OutputCost:  f64ptr(1.5),
		Created:     "2020-01-01T00:00:00Z",
		Updated:     "2020-01-02T00:00:00Z",
	}, m, false)

	// Secret preserved (server never returns it).
	if m.APIKey.ValueString() != "sk-secret-xyz" {
		t.Errorf("apply() must preserve api_key, got %q", m.APIKey.ValueString())
	}
	// input/output cost refreshed from the server (always serialized).
	if m.InputCost.ValueFloat64() != 0.5 || m.OutputCost.ValueFloat64() != 1.5 {
		t.Errorf("apply() did not refresh input/output cost: in=%v out=%v", m.InputCost, m.OutputCost)
	}
	if m.MaxTokens.ValueInt64() != 8192 || m.Temperature.ValueFloat64() != 0.7 {
		t.Errorf("apply() clobbered max_tokens/temperature: %v %v", m.MaxTokens, m.Temperature)
	}
	if !m.HasReasoning.ValueBool() || !m.SupportsVision.ValueBool() || !m.SupportsToolCalling.ValueBool() {
		t.Errorf("apply() clobbered capability bools")
	}
	if m.SupportsStrictTool.ValueBool() || m.SupportsImageEdit.ValueBool() {
		t.Errorf("apply() flipped a false capability bool")
	}
	if m.CostPerImage.ValueFloat64() != 0.02 {
		t.Errorf("apply() clobbered cost_per_image: %v", m.CostPerImage)
	}

	// Identity fields refreshed from the server.
	if m.ID.ValueString() != "mdl_uuid_1" || m.DisplayName.ValueString() != "tf model" {
		t.Errorf("identity not refreshed: id=%q name=%q", m.ID.ValueString(), m.DisplayName.ValueString())
	}
	if m.ModelID.ValueString() != "liquid/lfm2.5-1.2b" || m.ModelType.ValueString() != "chat" {
		t.Errorf("model_id/model_type not refreshed: %q %q", m.ModelID.ValueString(), m.ModelType.ValueString())
	}
	if m.Region.ValueString() != "europe" || m.BaseURL.ValueString() != "http://host.docker.internal:1234/v1" {
		t.Errorf("region/base_url not refreshed: %q %q", m.Region.ValueString(), m.BaseURL.ValueString())
	}
	if m.Description.ValueString() != "a custom model" {
		t.Errorf("description should round-trip, got %q", m.Description.ValueString())
	}
	if m.Created.ValueString() != "2020-01-01T00:00:00Z" || m.Updated.ValueString() != "2020-01-02T00:00:00Z" {
		t.Errorf("timestamps not refreshed: %q %q", m.Created.ValueString(), m.Updated.ValueString())
	}
}

// TestModelApplyRegionBaseURLEmptyFallback proves a Required attribute never
// reads back empty when the server omits the (optional-on-the-wire)
// configuration.region / base_url: apply keeps the configured value.
func TestModelApplyRegionBaseURLEmptyFallback(t *testing.T) {
	r := &modelResource{}
	m := &modelResourceModel{
		Region:  types.StringValue("europe"),
		BaseURL: types.StringValue("http://host/v1"),
	}
	r.apply(&client.Model{ID: "mdl_1", Region: "", BaseURL: ""}, m, false)
	if m.Region.ValueString() != "europe" {
		t.Errorf("empty server region must fall back to config, got %q", m.Region.ValueString())
	}
	if m.BaseURL.ValueString() != "http://host/v1" {
		t.Errorf("empty server base_url must fall back to config, got %q", m.BaseURL.ValueString())
	}
}

func f64ptr(f float64) *float64 { return &f }
func bptr(b bool) *bool         { return &b }

// TestModelApplyRefreshesListFields proves the cost / capability fields ARE
// refreshed from the server when present, so out-of-band drift is visible.
func TestModelApplyRefreshesListFields(t *testing.T) {
	r := &modelResource{}
	// Prior/plan values that the server has since changed out of band.
	m := &modelResourceModel{
		InputCost:      types.Float64Value(0.1),
		OutputCost:     types.Float64Value(0.2),
		CostPerImage:   types.Float64Value(0.01),
		SupportsVision: types.BoolValue(false),
	}
	r.apply(&client.Model{
		ID:             "mdl_1",
		Provider:       client.ModelProviderOpenAILike,
		InputCost:      f64ptr(0.9),
		OutputCost:     f64ptr(1.8),
		CostPerImage:   f64ptr(0.05),
		SupportsVision: bptr(true),
	}, m, false)
	if m.InputCost.ValueFloat64() != 0.9 || m.OutputCost.ValueFloat64() != 1.8 {
		t.Errorf("costs not refreshed from server: in=%v out=%v", m.InputCost, m.OutputCost)
	}
	if m.CostPerImage.ValueFloat64() != 0.05 {
		t.Errorf("cost_per_image not refreshed: %v", m.CostPerImage)
	}
	if !m.SupportsVision.ValueBool() {
		t.Error("supports_vision not refreshed from server")
	}
}

// TestModelApplyCollapsesUnknownToNull proves a create-time unknown (Optional+
// Computed, omitted in config) collapses to a concrete null when the server also
// omits the field — a post-apply state must never carry an unknown.
func TestModelApplyCollapsesUnknownToNull(t *testing.T) {
	r := &modelResource{}
	m := &modelResourceModel{
		InputCost:         types.Float64Unknown(),
		MaxTokens:         types.Int64Unknown(),
		Temperature:       types.Float64Unknown(),
		HasReasoning:      types.BoolUnknown(),
		SupportsImageEdit: types.BoolUnknown(),
	}
	// Server omits all of them (nil).
	r.apply(&client.Model{ID: "mdl_1", Provider: client.ModelProviderOpenAILike}, m, false)
	for name, isNull := range map[string]bool{
		"input_cost":          m.InputCost.IsNull(),
		"max_tokens":          m.MaxTokens.IsNull(),
		"temperature":         m.Temperature.IsNull(),
		"has_reasoning":       m.HasReasoning.IsNull(),
		"supports_image_edit": m.SupportsImageEdit.IsNull(),
	} {
		if !isNull {
			t.Errorf("%s must collapse unknown → null when the server omits it", name)
		}
	}
}

// TestModelApplyRefreshVsRetain locks in the FIX 3 decision table: input_cost /
// output_cost are ALWAYS on the wire (no omitempty) so they refresh unconditionally
// — a non-zero → 0 out-of-band change surfaces; cost_per_image and the supports_*
// bools are metadata `omitempty` fields whose false/0 is DROPPED on the wire, so on
// absence (nil) the prior value is retained (an out-of-band flip is not surfaced).
func TestModelApplyRefreshVsRetain(t *testing.T) {
	r := &modelResource{}
	m := &modelResourceModel{
		InputCost:           types.Float64Value(5),
		OutputCost:          types.Float64Value(10),
		CostPerImage:        types.Float64Value(0.02),
		SupportsVision:      types.BoolValue(true),
		SupportsToolCalling: types.BoolValue(true),
		SupportsStrictTool:  types.BoolValue(true),
		SupportsImageEdit:   types.BoolValue(true),
	}
	// Server: costs dropped to 0 (still present on the wire); every metadata
	// omitempty field absent (nil) because it is now false/0.
	r.apply(&client.Model{
		ID:         "mdl_1",
		Provider:   client.ModelProviderOpenAILike,
		InputCost:  f64ptr(0),
		OutputCost: f64ptr(0),
		// CostPerImage / Supports* left nil (server omitted them).
	}, m, false)

	if m.InputCost.ValueFloat64() != 0 || m.OutputCost.ValueFloat64() != 0 {
		t.Errorf("input/output cost must refresh to 0 (non-zero → 0 surfaces): in=%v out=%v", m.InputCost, m.OutputCost)
	}
	if m.InputCost.IsNull() || m.OutputCost.IsNull() {
		t.Error("input/output cost 0 must be a concrete 0, not null")
	}
	if m.CostPerImage.ValueFloat64() != 0.02 {
		t.Errorf("cost_per_image must be RETAINED on server absence, got %v", m.CostPerImage)
	}
	if !m.SupportsVision.ValueBool() || !m.SupportsToolCalling.ValueBool() ||
		!m.SupportsStrictTool.ValueBool() || !m.SupportsImageEdit.ValueBool() {
		t.Error("supports_* must be RETAINED on server absence (an out-of-band flip to false is not surfaced)")
	}
}

// TestValidateImageModelCosts proves the FINDING 4 guardrail: input_cost/output_cost
// set on an image model is rejected (the server ignores per-token costs there and
// stores 0, breaking plan consistency), while a non-image model or an image model that
// only sets cost_per_image (input/output null or unknown) is accepted.
func TestValidateImageModelCosts(t *testing.T) {
	img := types.StringValue("image")
	cases := []struct {
		name       string
		modelType  types.String
		inputCost  types.Float64
		outputCost types.Float64
		wantError  bool
	}{
		{"image + input_cost rejected", img, types.Float64Value(0.5), types.Float64Null(), true},
		{"image + output_cost rejected", img, types.Float64Null(), types.Float64Value(0.5), true},
		{"image + both rejected", img, types.Float64Value(0.1), types.Float64Value(0.2), true},
		{"image without token costs ok", img, types.Float64Null(), types.Float64Null(), false},
		{"image + unknown costs defers (ok)", img, types.Float64Unknown(), types.Float64Unknown(), false},
		{"chat + costs ok", types.StringValue("chat"), types.Float64Value(0.5), types.Float64Value(1.0), false},
		{"embedding + input_cost ok", types.StringValue("embedding"), types.Float64Value(0.5), types.Float64Null(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validateImageModelCosts(tc.modelType, tc.inputCost, tc.outputCost)
			if d.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v (%v)", d.HasError(), tc.wantError, d)
			}
		})
	}
}

// TestModelApplyPreservesPlannedCostOnCreate proves the FINDING 4 protocol fix: on the
// CREATE/UPDATE path (preservePlannedCosts=true) a KNOWN planned input_cost/output_cost
// is PRESERVED even when the server normalizes it to a different value (e.g. 0 for a
// cost it ignores), so post-apply state equals the plan and no "inconsistent result
// after apply" is raised. A null/unknown planned cost is still filled from the server.
func TestModelApplyPreservesPlannedCostOnCreate(t *testing.T) {
	r := &modelResource{}

	t.Run("known planned cost preserved over server normalization", func(t *testing.T) {
		m := &modelResourceModel{
			InputCost:  types.Float64Value(0.5),
			OutputCost: types.Float64Value(1.5),
		}
		// Server normalized both to 0 (as it would for a cost it ignores).
		r.apply(&client.Model{ID: "mdl_1", InputCost: f64ptr(0), OutputCost: f64ptr(0)}, m, true)
		if m.InputCost.ValueFloat64() != 0.5 || m.OutputCost.ValueFloat64() != 1.5 {
			t.Errorf("known planned cost must be preserved on create/update: in=%v out=%v", m.InputCost, m.OutputCost)
		}
	})

	t.Run("null/unknown planned cost filled from server", func(t *testing.T) {
		m := &modelResourceModel{
			InputCost:  types.Float64Unknown(),
			OutputCost: types.Float64Null(),
		}
		r.apply(&client.Model{ID: "mdl_1", InputCost: f64ptr(0.9), OutputCost: f64ptr(1.8)}, m, true)
		if m.InputCost.ValueFloat64() != 0.9 || m.OutputCost.ValueFloat64() != 1.8 {
			t.Errorf("null/unknown planned cost must be filled from server: in=%v out=%v", m.InputCost, m.OutputCost)
		}
		if m.InputCost.IsNull() || m.InputCost.IsUnknown() || m.OutputCost.IsNull() {
			t.Error("filled cost must be a concrete value")
		}
	})
}

// TestModelTypeEnum proves model_type accepts only the four values the server's
// openai-like endpoints validate (chat/completion/embedding/image) and rejects the
// previously-permitted extras (rerank/stt/tts/moderation/realtime), which would
// deterministically fail at apply.
func TestModelTypeEnum(t *testing.T) {
	ctx := context.Background()
	var sch resource.SchemaResponse
	NewModelResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	attr, ok := sch.Schema.Attributes["model_type"].(schema.StringAttribute)
	if !ok {
		t.Fatal("model_type is not a StringAttribute")
	}
	rejected := func(v string) bool {
		req := validator.StringRequest{Path: path.Root("model_type"), ConfigValue: types.StringValue(v)}
		resp := &validator.StringResponse{}
		for _, val := range attr.Validators {
			val.ValidateString(ctx, req, resp)
		}
		return resp.Diagnostics.HasError()
	}
	for _, v := range []string{"chat", "completion", "embedding", "image"} {
		if rejected(v) {
			t.Errorf("%q must be an accepted model_type", v)
		}
	}
	for _, v := range []string{"rerank", "stt", "tts", "moderation", "realtime", "video", "ocr", ""} {
		if !rejected(v) {
			t.Errorf("%q must be rejected (server accepts only chat/completion/embedding/image)", v)
		}
	}
}

// TestBaseURLRejectsEmbeddedCredentials proves the validator rejects userinfo and
// any query string — both can smuggle a credential into the not-Sensitive,
// server-logged base_url — while a clean absolute http(s) URL is accepted.
func TestBaseURLRejectsEmbeddedCredentials(t *testing.T) {
	ctx := context.Background()
	check := func(s string) bool {
		req := validator.StringRequest{Path: path.Root("base_url"), ConfigValue: types.StringValue(s)}
		resp := &validator.StringResponse{}
		absoluteHTTPURLValidator{}.ValidateString(ctx, req, resp)
		return resp.Diagnostics.HasError()
	}
	reject := []string{
		"https://user:pass@host/v1",      // userinfo with password
		"https://token@host/v1",          // userinfo (username only)
		"https://host/v1?api-key=secret", // credential-bearing query
		"https://host/v1?foo=bar",        // any query at all
		"https://host/v1?",               // ForceQuery (bare '?')
	}
	for _, s := range reject {
		if !check(s) {
			t.Errorf("%q must be rejected (embedded credentials / query string)", s)
		}
	}
	accept := []string{
		"https://host/v1",
		"http://host:1234/v1",
		"https://a.b.example.com/openai/v1",
		"https://host/v1/", // trailing-slash path is fine
	}
	for _, s := range accept {
		if check(s) {
			t.Errorf("%q must be accepted", s)
		}
	}
}

// TestModelNotCustom proves the custom-vs-system guard: a system / non-openai-like
// model is rejected, a custom openai-like model is accepted.
func TestModelNotCustom(t *testing.T) {
	// System model (owner "system", real provider) → refused.
	if _, _, notCustom := modelNotCustom(&client.Model{ID: "openai/gpt-4o", Provider: "openai", Owner: "system"}); !notCustom {
		t.Error("a system model must be rejected as not-custom")
	}
	// Custom model of a different provider type → refused.
	if _, _, notCustom := modelNotCustom(&client.Model{ID: "u1", Provider: "aws", Owner: "ws_1"}); !notCustom {
		t.Error("a non-openai-like custom model must be rejected")
	}
	// Custom openai-like model → accepted.
	if _, _, notCustom := modelNotCustom(&client.Model{ID: "u2", Provider: client.ModelProviderOpenAILike, Owner: "ws_1"}); notCustom {
		t.Error("a custom openai-like model must be accepted")
	}
}

// TestAbsoluteHTTPURLValidator locks in the base_url validation.
func TestAbsoluteHTTPURLValidator(t *testing.T) {
	ctx := context.Background()
	ok := []string{"https://host/v1", "http://host:1234/v1", "https://a.b.example.com/openai/v1"}
	bad := []string{"host/v1", "ftp://host/v1", "/v1", "://nope", "not a url"}
	check := func(s string) bool {
		req := validator.StringRequest{Path: path.Root("base_url"), ConfigValue: types.StringValue(s)}
		resp := &validator.StringResponse{}
		absoluteHTTPURLValidator{}.ValidateString(ctx, req, resp)
		return resp.Diagnostics.HasError()
	}
	for _, s := range ok {
		if check(s) {
			t.Errorf("%q should be a valid base_url", s)
		}
	}
	for _, s := range bad {
		if !check(s) {
			t.Errorf("%q should be an invalid base_url", s)
		}
	}
	// A null / unknown value is not validated (skipped).
	req := validator.StringRequest{Path: path.Root("base_url"), ConfigValue: types.StringNull()}
	resp := &validator.StringResponse{}
	absoluteHTTPURLValidator{}.ValidateString(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Error("null base_url must be skipped by the validator")
	}
}

// TestModelSchemaDecisions locks in the update-vs-replace + secret decisions:
// api_key is Sensitive, Required and RequiresReplace; model_type constrains its
// enum; the identity fields (model_id/model_type/region/base_url) carry no
// RequiresReplace (they are mutable in place).
func TestModelSchemaDecisions(t *testing.T) {
	ctx := context.Background()
	var sch resource.SchemaResponse
	NewModelResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	if sch.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", sch.Diagnostics)
	}

	apiKey, ok := sch.Schema.Attributes["api_key"].(schema.StringAttribute)
	if !ok {
		t.Fatal("api_key is not a StringAttribute")
	}
	if !apiKey.Sensitive {
		t.Error("api_key must be Sensitive")
	}
	if !apiKey.Required {
		t.Error("api_key must be Required")
	}
	if len(apiKey.PlanModifiers) == 0 {
		t.Error("api_key must carry a RequiresReplace plan modifier (secret is immutable in place)")
	} else {
		desc := strings.ToLower(apiKey.PlanModifiers[0].Description(ctx))
		if !strings.Contains(desc, "destroy") || !strings.Contains(desc, "recreate") {
			t.Errorf("api_key plan modifier is not RequiresReplace: %q", desc)
		}
	}

	modelType, ok := sch.Schema.Attributes["model_type"].(schema.StringAttribute)
	if !ok {
		t.Fatal("model_type is not a StringAttribute")
	}
	if !modelType.Required || len(modelType.Validators) == 0 {
		t.Error("model_type must be Required with an enum validator")
	}

	// Fields the update endpoint accepts are mutable in place: no RequiresReplace.
	for _, name := range []string{"model_id", "model_type", "region", "base_url", "display_name"} {
		attr, ok := sch.Schema.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a StringAttribute", name)
		}
		for _, pm := range attr.PlanModifiers {
			d := strings.ToLower(pm.Description(ctx))
			if strings.Contains(d, "destroy") && strings.Contains(d, "recreate") {
				t.Errorf("%s must be mutable in place (no RequiresReplace)", name)
			}
		}
	}
}
