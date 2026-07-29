package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

func wsSchema(t *testing.T) schema.Schema {
	t.Helper()
	var sch resource.SchemaResponse
	NewWorkspaceSettingsResource().Schema(context.Background(), resource.SchemaRequest{}, &sch)
	if sch.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", sch.Diagnostics)
	}
	return sch.Schema
}

func wsPiiAttrs(t *testing.T) (schema.SingleNestedAttribute, map[string]schema.Attribute) {
	t.Helper()
	pii, ok := wsSchema(t).Attributes["pii_redaction"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatal("pii_redaction is not a SingleNestedAttribute")
	}
	cfg, ok := pii.Attributes["config"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatal("pii_redaction.config is not a SingleNestedAttribute")
	}
	return pii, cfg.Attributes
}

func TestWorkspaceSettingsSchemaDecisions(t *testing.T) {
	sch := wsSchema(t)

	if _, hasID := sch.Attributes["id"]; hasID {
		t.Error("a singleton must not carry an id attribute")
	}

	key, ok := sch.Attributes["key"].(schema.StringAttribute)
	if !ok {
		t.Fatal("key is not a StringAttribute")
	}
	if !key.Computed || key.Optional || key.Required {
		t.Error("key must be Computed-only (the slug is read-only server-side)")
	}

	name, ok := sch.Attributes["display_name"].(schema.StringAttribute)
	if !ok {
		t.Fatal("display_name is not a StringAttribute")
	}
	if !name.Optional || !name.Computed {
		t.Error("display_name must be Optional+Computed (omitting it adopts the server value)")
	}
	if len(name.Validators) != 2 {
		t.Errorf("display_name needs a length AND a whitespace validator, got %d", len(name.Validators))
	}

	enforce, ok := sch.Attributes["enforce_enabled_models"].(schema.BoolAttribute)
	if !ok {
		t.Fatal("enforce_enabled_models is not a BoolAttribute")
	}
	if !enforce.Optional || !enforce.Computed {
		t.Error("enforce_enabled_models must be Optional+Computed")
	}

	pii, cfgAttrs := wsPiiAttrs(t)
	if !pii.Optional {
		t.Error("pii_redaction must be Optional")
	}
	if pii.Computed {
		t.Error("pii_redaction must NOT be Computed — omitted must mean UNMANAGED, never a server-sourced value")
	}
	if enabled, ok := pii.Attributes["enabled"].(schema.BoolAttribute); !ok || !enabled.Required {
		t.Error("pii_redaction.enabled must be Required")
	}

	for _, name := range []string{"language", "entities", "on_failure", "threshold"} {
		if _, ok := cfgAttrs[name]; !ok {
			t.Errorf("pii_redaction.config.%s is missing", name)
		}
	}
	if lang := cfgAttrs["language"].(schema.StringAttribute); !lang.Optional || len(lang.Validators) == 0 {
		t.Error("language must be Optional with a OneOf validator")
	}
	if onFailure := cfgAttrs["on_failure"].(schema.StringAttribute); !onFailure.Optional || len(onFailure.Validators) == 0 {
		t.Error("on_failure must be Optional with a OneOf validator")
	}
	if threshold := cfgAttrs["threshold"].(schema.Float64Attribute); !threshold.Optional || len(threshold.Validators) == 0 {
		t.Error("threshold must be Optional with a Between(0,1) validator")
	}
}

func TestWorkspaceSettingsEnumValidators(t *testing.T) {
	ctx := context.Background()
	_, cfgAttrs := wsPiiAttrs(t)

	checkString := func(attr string, value string, wantErr bool) {
		t.Helper()
		for _, v := range cfgAttrs[attr].(schema.StringAttribute).Validators {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{
				Path:        path.Root("pii_redaction").AtName("config").AtName(attr),
				ConfigValue: types.StringValue(value),
			}, &resp)
			if resp.Diagnostics.HasError() {
				if !wantErr {
					t.Errorf("%s=%q rejected: %v", attr, value, resp.Diagnostics)
				}
				return
			}
		}
		if wantErr {
			t.Errorf("%s=%q was accepted, want rejected", attr, value)
		}
	}
	checkString("language", "en", false)
	checkString("language", "nl", false)
	checkString("language", "de", true)
	checkString("on_failure", "block", false)
	checkString("on_failure", "passthrough", false)
	checkString("on_failure", "ignore", true)

	checkFloat := func(value float64, wantErr bool) {
		t.Helper()
		for _, v := range cfgAttrs["threshold"].(schema.Float64Attribute).Validators {
			var resp validator.Float64Response
			v.ValidateFloat64(ctx, validator.Float64Request{
				Path:        path.Root("pii_redaction").AtName("config").AtName("threshold"),
				ConfigValue: types.Float64Value(value),
			}, &resp)
			if resp.Diagnostics.HasError() {
				if !wantErr {
					t.Errorf("threshold=%v rejected: %v", value, resp.Diagnostics)
				}
				return
			}
		}
		if wantErr {
			t.Errorf("threshold=%v was accepted, want rejected", value)
		}
	}
	checkFloat(0, false)
	checkFloat(1, false)
	checkFloat(0.35, false)
	checkFloat(1.5, true)
	checkFloat(-0.1, true)
}

// Only SURROUNDING whitespace is rejected; interior Unicode spacing is a
// legitimate name and must pass.
func TestWorkspaceSettingsDisplayNameWhitespaceRejected(t *testing.T) {
	ctx := context.Background()
	name := wsSchema(t).Attributes["display_name"].(schema.StringAttribute)

	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"plain", "orq-test", false},
		{"interior ascii space", "Acme Inc", false},
		{"interior newline", "multi\nline", false},
		{"interior nbsp", "Acme\u00a0Inc", false},
		{"interior em space", "Acme\u2003Inc", false},
		{"non-ascii letters", "Ünïcodé Wörkspace", false},
		{"128 two-byte runes", strings.Repeat("é", 128), false},
		{"129 two-byte runes", strings.Repeat("é", 129), true},
		{"leading ascii space", " orq-test", true},
		{"trailing ascii space", "orq-test ", true},
		{"ascii whitespace only", "   ", true},
		{"empty", "", true}, // length validator
		{"leading nbsp", "\u00a0Acme", true},
		{"trailing nbsp", "Acme\u00a0", true},
		{"leading em space", "\u2003Acme", true},
		{"trailing em space", "Acme\u2003", true},
		{"nbsp both sides", "\u00a0Acme\u00a0", true},
		{"unicode whitespace only", "\u00a0\u2003", true},
		{"trailing ideographic space", "Acme\u3000", true},
	}
	for _, tc := range cases {
		var gotErr bool
		for _, v := range name.Validators {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{
				Path:        path.Root("display_name"),
				ConfigValue: types.StringValue(tc.value),
			}, &resp)
			if resp.Diagnostics.HasError() {
				gotErr = true
			}
		}
		if gotErr != tc.wantErr {
			t.Errorf("%s: display_name %q: error = %v, want %v", tc.name, tc.value, gotErr, tc.wantErr)
		}
	}

	// The diagnostic must name the canonical spelling: the padding is invisible.
	var resp validator.StringResponse
	displayNameWhitespaceValidator{}.ValidateString(ctx, validator.StringRequest{
		Path:        path.Root("display_name"),
		ConfigValue: types.StringValue(" Acme Inc "),
	}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("padded name accepted")
	}
	if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, `"Acme Inc"`) {
		t.Errorf("diagnostic must suggest the trimmed value, got %q", detail)
	}

	// Null/unknown are ordinary plan states for an Optional+Computed attribute.
	for _, v := range []types.String{types.StringNull(), types.StringUnknown()} {
		var resp validator.StringResponse
		displayNameWhitespaceValidator{}.ValidateString(ctx, validator.StringRequest{
			Path:        path.Root("display_name"),
			ConfigValue: v,
		}, &resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("%v must be skipped by the validator, got %v", v, resp.Diagnostics)
		}
	}
}

func TestWorkspaceSettingsUpdateInputOmitsUnmanagedFields(t *testing.T) {
	ctx := context.Background()

	// A create-time plan for a config that sets nothing.
	plan := workspaceSettingsResourceModel{
		DisplayName:          types.StringUnknown(),
		EnforceEnabledModels: types.BoolUnknown(),
	}
	in, diags := plan.updateInput(ctx)
	if diags.HasError() {
		t.Fatalf("updateInput: %v", diags)
	}
	if in.DisplayName != nil || in.EnforceEnabledModels != nil || in.PiiRedaction != nil {
		t.Fatalf("an unmanaged config must write nothing, got %+v", in)
	}
	if !in.IsEmpty() {
		t.Error("IsEmpty must report an all-unmanaged input as empty (it is answered with a read)")
	}

	// Managing only enforce_enabled_models must not drag pii_redaction along.
	plan = workspaceSettingsResourceModel{
		DisplayName:          types.StringNull(),
		EnforceEnabledModels: types.BoolValue(false),
	}
	in, _ = plan.updateInput(ctx)
	if in.PiiRedaction != nil {
		t.Error("omitted pii_redaction must not be sent (it is NOT enabled=false)")
	}
	if in.EnforceEnabledModels == nil || *in.EnforceEnabledModels {
		t.Errorf("explicit false must be written, got %v", in.EnforceEnabledModels)
	}
	if in.IsEmpty() {
		t.Error("an input carrying a value is not empty")
	}
}

// The server replaces the whole stored pii_redaction object with the payload, so
// a field the operator removed must be sent as ABSENT rather than retained.
func TestWorkspaceSettingsUpdateInputFullReplace(t *testing.T) {
	ctx := context.Background()
	plan := workspaceSettingsResourceModel{
		DisplayName:          types.StringNull(),
		EnforceEnabledModels: types.BoolNull(),
		PiiRedaction: &workspaceSettingsPiiModel{
			Enabled: types.BoolValue(true),
			Config: &workspaceSettingsPiiConfigModel{
				Language:  types.StringValue("en"),
				Entities:  stringListValue([]string{"EMAIL_ADDRESS"}),
				OnFailure: types.StringValue("block"),
				Threshold: types.Float64Null(), // removed from config
			},
		},
	}
	in, diags := plan.updateInput(ctx)
	if diags.HasError() {
		t.Fatalf("updateInput: %v", diags)
	}
	if in.PiiRedaction == nil || in.PiiRedaction.Config == nil {
		t.Fatal("managed pii block was not built")
	}
	cfg := in.PiiRedaction.Config
	if cfg.Threshold != nil {
		t.Error("a threshold removed from config must be sent as absent (full replace), not retained")
	}
	if cfg.Language == nil || *cfg.Language != "en" {
		t.Errorf("language = %v", cfg.Language)
	}
	if len(cfg.Entities) != 1 || cfg.Entities[0] != "EMAIL_ADDRESS" {
		t.Errorf("entities = %v", cfg.Entities)
	}

	// A block without a config sub-block stores an enable flag alone.
	plan.PiiRedaction.Config = nil
	in, _ = plan.updateInput(ctx)
	if in.PiiRedaction == nil || in.PiiRedaction.Config != nil {
		t.Error("an omitted config sub-block must be sent as absent")
	}
}

func TestWorkspaceSettingsApplyRetainsUnmanagedPii(t *testing.T) {
	srv := &client.WorkspaceSettings{
		Key:                  "acme",
		DisplayName:          "Acme",
		EnforceEnabledModels: true,
		PiiRedaction: &client.PiiRedaction{
			Enabled: true,
			Config:  &client.PiiRedactionConfig{Language: ptr("nl")},
		},
	}
	state := workspaceSettingsResourceModel{} // pii_redaction absent = unmanaged

	applyReadSettings(srv, &state)
	if state.PiiRedaction != nil {
		t.Fatal("an unmanaged pii_redaction must stay null in state (retain-on-null)")
	}
	if state.Key.ValueString() != "acme" || state.DisplayName.ValueString() != "Acme" || !state.EnforceEnabledModels.ValueBool() {
		t.Errorf("scalars were not refreshed: %+v", state)
	}
}

// The server stores NO entities key for an empty list, so the planned/prior value
// is what tells `entities = []` apart from an omitted attribute.
func TestWorkspaceSettingsApplyEmptyEntities(t *testing.T) {
	srvEmpty := &client.WorkspaceSettings{
		Key: "acme",
		PiiRedaction: &client.PiiRedaction{
			Enabled: true,
			Config:  &client.PiiRedactionConfig{Language: ptr("en")}, // no entities key
		},
	}

	// config said [] -> state must stay [] (non-null), not collapse to null.
	planned := workspaceSettingsResourceModel{PiiRedaction: &workspaceSettingsPiiModel{
		Enabled: types.BoolValue(true),
		Config:  &workspaceSettingsPiiConfigModel{Entities: stringListValue(nil)},
	}}
	if diags := applyWrittenSettings(srvEmpty, &planned); diags.HasError() {
		t.Fatalf("applyWrittenSettings: %v", diags)
	}
	got := planned.PiiRedaction.Config.Entities
	if got.IsNull() {
		t.Error("entities = [] must survive the round trip as [] (the server omits the key)")
	}
	if len(got.Elements()) != 0 {
		t.Errorf("entities = %v, want []", got)
	}

	// config omitted entities -> state must stay null.
	omitted := workspaceSettingsResourceModel{PiiRedaction: &workspaceSettingsPiiModel{
		Enabled: types.BoolValue(true),
		Config:  &workspaceSettingsPiiConfigModel{Entities: types.ListNull(types.StringType)},
	}}
	if diags := applyWrittenSettings(srvEmpty, &omitted); diags.HasError() {
		t.Fatalf("applyWrittenSettings: %v", diags)
	}
	if !omitted.PiiRedaction.Config.Entities.IsNull() {
		t.Error("an omitted entities attribute must stay null (never become [])")
	}

	// A server list always wins, so an out-of-band change surfaces as drift.
	srvList := &client.WorkspaceSettings{Key: "acme", PiiRedaction: &client.PiiRedaction{
		Enabled: true,
		Config:  &client.PiiRedactionConfig{Entities: []string{"PERSON"}},
	}}
	applyReadSettings(srvList, &omitted)
	if len(omitted.PiiRedaction.Config.Entities.Elements()) != 1 {
		t.Errorf("a server entities list must win on read, got %v", omitted.PiiRedaction.Config.Entities)
	}
}

// Overwriting a known planned nested value with the read-back is what Terraform
// aborts as "Provider produced inconsistent result after apply".
func TestWorkspaceSettingsApplyPiiFields(t *testing.T) {
	// The response deliberately DISAGREES with the plan on every field, and
	// carries an extra one the plan does not have.
	srv := &client.WorkspaceSettings{Key: "acme", PiiRedaction: &client.PiiRedaction{
		Enabled: false,
		Config: &client.PiiRedactionConfig{
			Language:  ptr("nl"),
			Entities:  []string{"person", "EMAIL_ADDRESS"}, // re-cased AND re-ordered
			OnFailure: ptr("passthrough"),
			Threshold: ptrFloat(0.4),
		},
	}}
	m := workspaceSettingsResourceModel{PiiRedaction: &workspaceSettingsPiiModel{
		Enabled: types.BoolValue(true),
		Config: &workspaceSettingsPiiConfigModel{
			Language: types.StringValue("en"),
			Entities: stringListValue([]string{"EMAIL_ADDRESS", "PERSON"}),
			// on_failure and threshold are absent from the block: a KNOWN null,
			// which must stay null rather than adopt the server's value.
		},
	}}
	if diags := applyWrittenSettings(srv, &m); diags.HasError() {
		t.Fatalf("applyWrittenSettings: %v", diags)
	}
	if !m.PiiRedaction.Enabled.ValueBool() {
		t.Error("enabled: the planned value must win on the write path")
	}
	cfg := m.PiiRedaction.Config
	if cfg.Language.ValueString() != "en" {
		t.Errorf("language = %v, want the planned \"en\"", cfg.Language)
	}
	gotEntities := listStrings(t, cfg.Entities)
	if len(gotEntities) != 2 || gotEntities[0] != "EMAIL_ADDRESS" || gotEntities[1] != "PERSON" {
		t.Errorf("entities = %v, want the planned order and casing [EMAIL_ADDRESS PERSON]", gotEntities)
	}
	if !cfg.OnFailure.IsNull() {
		t.Errorf("on_failure = %v: a planned null must not be filled from the read-back", cfg.OnFailure)
	}
	if !cfg.Threshold.IsNull() {
		t.Errorf("threshold = %v: a planned null must not be filled from the read-back", cfg.Threshold)
	}

	noCfg := workspaceSettingsResourceModel{PiiRedaction: &workspaceSettingsPiiModel{Enabled: types.BoolValue(false)}}
	if diags := applyWrittenSettings(srv, &noCfg); diags.HasError() {
		t.Fatalf("applyWrittenSettings: %v", diags)
	}
	if noCfg.PiiRedaction.Config != nil {
		t.Error("the write path must not invent a config sub-block the plan does not have")
	}

	// Only a genuinely UNKNOWN planned value may be filled from the response.
	unknown := workspaceSettingsResourceModel{PiiRedaction: &workspaceSettingsPiiModel{
		Enabled: types.BoolValue(true),
		Config: &workspaceSettingsPiiConfigModel{
			Language:  types.StringUnknown(),
			Entities:  types.ListUnknown(types.StringType),
			OnFailure: types.StringUnknown(),
			Threshold: types.Float64Unknown(),
		},
	}}
	if diags := applyWrittenSettings(srv, &unknown); diags.HasError() {
		t.Fatalf("applyWrittenSettings: %v", diags)
	}
	ucfg := unknown.PiiRedaction.Config
	if ucfg.Language.ValueString() != "nl" || ucfg.OnFailure.ValueString() != "passthrough" {
		t.Errorf("an unknown planned value must be filled from the read-back: %+v", ucfg)
	}
	if ucfg.Threshold.IsUnknown() || ucfg.Threshold.ValueFloat64() != 0.4 {
		t.Errorf("threshold = %v", ucfg.Threshold)
	}
	if ucfg.Entities.IsUnknown() {
		t.Error("post-apply state must be wholly known, entities is still unknown")
	}
	if got := listStrings(t, ucfg.Entities); len(got) != 2 || got[0] != "person" {
		t.Errorf("unknown entities must be filled from the read-back verbatim, got %v", got)
	}

	// A KNOWN list carrying an UNKNOWN element is not fully known either.
	partial := workspaceSettingsResourceModel{PiiRedaction: &workspaceSettingsPiiModel{
		Enabled: types.BoolValue(true),
		Config: &workspaceSettingsPiiConfigModel{
			Entities: types.ListValueMust(types.StringType, []attr.Value{
				types.StringValue("PERSON"), types.StringUnknown(),
			}),
		},
	}}
	if diags := applyWrittenSettings(srv, &partial); diags.HasError() {
		t.Fatalf("applyWrittenSettings: %v", diags)
	}
	pents := partial.PiiRedaction.Config.Entities
	if pents.IsUnknown() {
		t.Error("post-apply state must be wholly known, entities is still unknown")
	}
	for _, e := range pents.Elements() {
		if e.IsUnknown() {
			t.Error("post-apply state must be wholly known, an entities element is still unknown")
		}
	}
	if got := listStrings(t, pents); len(got) != 2 || got[0] != "person" {
		t.Errorf("a partially-unknown planned list must resolve from the read-back, got %v", got)
	}
}

func TestWorkspaceSettingsApplyPiiFieldsReadPath(t *testing.T) {
	srv := &client.WorkspaceSettings{Key: "acme", PiiRedaction: &client.PiiRedaction{
		Enabled: false,
		Config: &client.PiiRedactionConfig{
			Language:  ptr("nl"),
			Entities:  []string{"PERSON"},
			OnFailure: ptr("passthrough"),
			Threshold: ptrFloat(0.4),
		},
	}}
	state := workspaceSettingsResourceModel{PiiRedaction: &workspaceSettingsPiiModel{
		Enabled: types.BoolValue(true),
		Config: &workspaceSettingsPiiConfigModel{
			Language: types.StringValue("en"),
			Entities: stringListValue([]string{"EMAIL_ADDRESS"}),
		},
	}}
	applyReadSettings(srv, &state)
	if state.PiiRedaction.Enabled.ValueBool() {
		t.Error("enabled: the server must win on read")
	}
	cfg := state.PiiRedaction.Config
	if cfg.Language.ValueString() != "nl" || cfg.OnFailure.ValueString() != "passthrough" {
		t.Errorf("config not refreshed from the server: %+v", cfg)
	}
	if cfg.Threshold.IsNull() || cfg.Threshold.ValueFloat64() != 0.4 {
		t.Errorf("threshold = %v, want the server's 0.4", cfg.Threshold)
	}
	if got := listStrings(t, cfg.Entities); len(got) != 1 || got[0] != "PERSON" {
		t.Errorf("entities = %v, want the server's [PERSON]", got)
	}

	// An absent optional field lands as null, never as a zero value.
	srvSparse := &client.WorkspaceSettings{Key: "acme", PiiRedaction: &client.PiiRedaction{
		Enabled: true,
		Config:  &client.PiiRedactionConfig{Language: ptr("en")},
	}}
	applyReadSettings(srvSparse, &state)
	cfg = state.PiiRedaction.Config
	if !cfg.OnFailure.IsNull() || !cfg.Threshold.IsNull() {
		t.Errorf("absent optional fields must read back null, got on_failure=%v threshold=%v", cfg.OnFailure, cfg.Threshold)
	}

	// An enable flag stored without a config object reads back with no config.
	srvNoCfg := &client.WorkspaceSettings{Key: "acme", PiiRedaction: &client.PiiRedaction{Enabled: false}}
	applyReadSettings(srvNoCfg, &state)
	if state.PiiRedaction.Config != nil {
		t.Error("a stored block with no config must read back with a null config")
	}
	if state.PiiRedaction.Enabled.ValueBool() {
		t.Error("enabled must follow the server value on read")
	}
}

// listStrings flattens a types.List of strings, failing on an unknown element.
func listStrings(t *testing.T, l types.List) []string {
	t.Helper()
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := make([]string, 0, len(l.Elements()))
	for _, e := range l.Elements() {
		s, ok := e.(types.String)
		if !ok {
			t.Fatalf("element %v is not a string", e)
		}
		if s.IsUnknown() {
			t.Fatalf("element %v is unknown", e)
		}
		out = append(out, s.ValueString())
	}
	return out
}

func TestWorkspaceSettingsApplyPiiMissingFromReadBack(t *testing.T) {
	srv := &client.WorkspaceSettings{Key: "acme"} // no pii_redaction at all

	readState := workspaceSettingsResourceModel{PiiRedaction: &workspaceSettingsPiiModel{Enabled: types.BoolValue(true)}}
	applyReadSettings(srv, &readState)
	if readState.PiiRedaction != nil {
		t.Error("read must drop a managed block the server no longer has (drift)")
	}

	writeState := workspaceSettingsResourceModel{PiiRedaction: &workspaceSettingsPiiModel{Enabled: types.BoolValue(true)}}
	diags := applyWrittenSettings(srv, &writeState)
	if diags.HasError() {
		t.Fatalf("applyWrittenSettings: %v", diags)
	}
	if writeState.PiiRedaction == nil {
		t.Error("write must keep the planned block (post-apply state must equal the plan)")
	}
	if diags.WarningsCount() != 1 {
		t.Errorf("expected exactly one warning, got %v", diags)
	}
}

func ptr(s string) *string        { return &s }
func ptrFloat(f float64) *float64 { return &f }

// --- lifecycle: adopt on create, state-only delete -------------------------

type fakeWorkspaceSettingsAPI struct {
	settings *client.WorkspaceSettings
	gets     int
	updates  int
	lastIn   client.WorkspaceSettingsUpdateInput
}

func (f *fakeWorkspaceSettingsAPI) Get(_ context.Context) (*client.WorkspaceSettings, error) {
	f.gets++
	return f.settings, nil
}

func (f *fakeWorkspaceSettingsAPI) Update(_ context.Context, in client.WorkspaceSettingsUpdateInput) (*client.WorkspaceSettings, error) {
	f.updates++
	f.lastIn = in
	if in.DisplayName != nil {
		f.settings.DisplayName = *in.DisplayName
	}
	if in.EnforceEnabledModels != nil {
		f.settings.EnforceEnabledModels = *in.EnforceEnabledModels
	}
	if in.PiiRedaction != nil {
		f.settings.PiiRedaction = in.PiiRedaction
	}
	return f.settings, nil
}

func wsData(t *testing.T, m workspaceSettingsResourceModel) (tfsdk.Plan, tfsdk.Config, tfsdk.State) {
	t.Helper()
	ctx := context.Background()
	sch := wsSchema(t)
	plan := tfsdk.Plan{Schema: sch}
	if diags := plan.Set(ctx, &m); diags.HasError() {
		t.Fatalf("plan.Set: %v", diags)
	}
	cfg := tfsdk.Config{Schema: sch, Raw: plan.Raw}
	state := tfsdk.State{Schema: sch, Raw: plan.Raw}
	return plan, cfg, state
}

func TestWorkspaceSettingsCreateAdopts(t *testing.T) {
	ctx := context.Background()
	api := &fakeWorkspaceSettingsAPI{settings: &client.WorkspaceSettings{
		Key:                  "acme",
		DisplayName:          "Old Name",
		EnforceEnabledModels: true,
		PiiRedaction:         &client.PiiRedaction{Enabled: true, Config: &client.PiiRedactionConfig{Language: ptr("nl")}},
	}}
	r := &workspaceSettingsResource{settings: api}

	// Config sets display_name only; the rest are unknown on create.
	plan, cfg, state := wsData(t, workspaceSettingsResourceModel{
		Key:                  types.StringUnknown(),
		DisplayName:          types.StringValue("orq-test"),
		EnforceEnabledModels: types.BoolUnknown(),
	})
	resp := resource.CreateResponse{State: state}
	r.Create(ctx, resource.CreateRequest{Plan: plan, Config: cfg}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}

	if api.updates != 1 || api.gets != 0 {
		t.Errorf("adopt must be a single update, got updates=%d gets=%d", api.updates, api.gets)
	}
	if api.lastIn.DisplayName == nil || *api.lastIn.DisplayName != "orq-test" {
		t.Errorf("display_name not written: %v", api.lastIn.DisplayName)
	}
	if api.lastIn.EnforceEnabledModels != nil {
		t.Error("an unconfigured enforce_enabled_models must not be written")
	}
	if api.lastIn.PiiRedaction != nil {
		t.Error("an unconfigured pii_redaction must not be written")
	}

	var got workspaceSettingsResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("state.Get: %v", diags)
	}
	if got.Key.ValueString() != "acme" {
		t.Errorf("key not adopted: %v", got.Key)
	}
	if got.DisplayName.ValueString() != "orq-test" {
		t.Errorf("display_name = %v", got.DisplayName)
	}
	if !got.EnforceEnabledModels.ValueBool() {
		t.Error("enforce_enabled_models must be adopted from the server")
	}
	if got.PiiRedaction != nil {
		t.Error("an unmanaged pii_redaction must stay null in state even when the server has one")
	}
}

// wsRaw renders a model into a tfsdk raw value so a test can hand Update a PLAN
// and a CONFIG that differ (wsData builds all three from one model).
func wsRaw(t *testing.T, m workspaceSettingsResourceModel) tftypes.Value {
	t.Helper()
	p := tfsdk.Plan{Schema: wsSchema(t)}
	if diags := p.Set(context.Background(), &m); diags.HasError() {
		t.Fatalf("plan.Set: %v", diags)
	}
	return p.Raw
}

// display_name is Optional+Computed with UseStateForUnknown, so a config that
// omits it plans as the PRIOR STATE, not as null: a plan-derived payload would
// re-send it and silently rename a workspace renamed out of band. The payload
// comes from the CONFIG; the plan still decides what lands in state.
func TestWorkspaceSettingsUpdateWritesConfigNotPlan(t *testing.T) {
	ctx := context.Background()
	api := &fakeWorkspaceSettingsAPI{settings: &client.WorkspaceSettings{
		Key:                  "acme",
		DisplayName:          "New", // renamed out of band
		EnforceEnabledModels: false,
	}}
	r := &workspaceSettingsResource{settings: api}

	sch := wsSchema(t)
	// Prior state: display_name "Old", adopted by an earlier apply.
	prior := workspaceSettingsResourceModel{
		Key:                  types.StringValue("acme"),
		DisplayName:          types.StringValue("Old"),
		EnforceEnabledModels: types.BoolValue(false),
	}
	// Config omits display_name (null) and flips the flag it DOES manage.
	cfgModel := workspaceSettingsResourceModel{
		Key:                  types.StringNull(),
		DisplayName:          types.StringNull(),
		EnforceEnabledModels: types.BoolValue(true),
	}
	// Plan: UseStateForUnknown carries the prior display_name forward.
	planModel := workspaceSettingsResourceModel{
		Key:                  types.StringValue("acme"),
		DisplayName:          types.StringValue("Old"),
		EnforceEnabledModels: types.BoolValue(true),
	}

	resp := resource.UpdateResponse{State: tfsdk.State{Schema: sch, Raw: wsRaw(t, prior)}}
	r.Update(ctx, resource.UpdateRequest{
		State:  tfsdk.State{Schema: sch, Raw: wsRaw(t, prior)},
		Plan:   tfsdk.Plan{Schema: sch, Raw: wsRaw(t, planModel)},
		Config: tfsdk.Config{Schema: sch, Raw: wsRaw(t, cfgModel)},
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Update: %v", resp.Diagnostics)
	}

	if api.updates != 1 {
		t.Fatalf("expected exactly one update, got %d", api.updates)
	}
	if api.lastIn.DisplayName != nil {
		t.Errorf("display_name is not in config and must be OMITTED from the payload, got %q "+
			"(the payload was derived from the plan, not the config)", *api.lastIn.DisplayName)
	}
	if api.lastIn.EnforceEnabledModels == nil || !*api.lastIn.EnforceEnabledModels {
		t.Errorf("the configured enforce_enabled_models must be written, got %v", api.lastIn.EnforceEnabledModels)
	}

	var got workspaceSettingsResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("state.Get: %v", diags)
	}
	// Post-apply state must equal the PLAN, not the server's "New".
	if got.DisplayName.ValueString() != "Old" {
		t.Errorf("state display_name = %v, want the planned \"Old\" (post-apply state must equal the plan)",
			got.DisplayName)
	}
	if !got.EnforceEnabledModels.ValueBool() {
		t.Errorf("state enforce_enabled_models = %v, want true", got.EnforceEnabledModels)
	}
}

// A read-only use of this resource must never need the workspace.update verb.
func TestWorkspaceSettingsCreateWithNoManagedAttributesReads(t *testing.T) {
	ctx := context.Background()
	api := &fakeWorkspaceSettingsAPI{settings: &client.WorkspaceSettings{Key: "acme", DisplayName: "Acme"}}
	r := &workspaceSettingsResource{settings: api}

	plan, cfg, state := wsData(t, workspaceSettingsResourceModel{
		Key:                  types.StringUnknown(),
		DisplayName:          types.StringUnknown(),
		EnforceEnabledModels: types.BoolUnknown(),
	})
	resp := resource.CreateResponse{State: state}
	r.Create(ctx, resource.CreateRequest{Plan: plan, Config: cfg}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	if api.updates != 0 || api.gets != 1 {
		t.Errorf("a config that manages nothing must only read, got updates=%d gets=%d", api.updates, api.gets)
	}
}

func TestWorkspaceSettingsDeleteIsStateOnly(t *testing.T) {
	ctx := context.Background()
	api := &fakeWorkspaceSettingsAPI{settings: &client.WorkspaceSettings{Key: "acme"}}
	r := &workspaceSettingsResource{settings: api}

	_, _, state := wsData(t, workspaceSettingsResourceModel{
		Key:                  types.StringValue("acme"),
		DisplayName:          types.StringValue("Acme"),
		EnforceEnabledModels: types.BoolValue(false),
	})
	var resp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete: %v", resp.Diagnostics)
	}
	if api.gets != 0 || api.updates != 0 {
		t.Errorf("destroy must not call the API, got gets=%d updates=%d", api.gets, api.updates)
	}
	if resp.Diagnostics.WarningsCount() != 1 {
		t.Errorf("destroy must warn that the settings were left unchanged, got %v", resp.Diagnostics)
	}
}

func TestWorkspaceSettingsImportSeedsKey(t *testing.T) {
	ctx := context.Background()
	r := &workspaceSettingsResource{settings: &fakeWorkspaceSettingsAPI{}}
	// The framework hands ImportState a state whose Raw is an explicit null.
	sch := wsSchema(t)
	resp := resource.ImportStateResponse{State: tfsdk.State{
		Schema: sch,
		Raw:    tftypes.NewValue(sch.Type().TerraformType(ctx), nil),
	}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "workspace"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("ImportState: %v", resp.Diagnostics)
	}
	var got workspaceSettingsResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("state.Get: %v", diags)
	}
	if got.Key.ValueString() != "workspace" {
		t.Errorf("import must seed key with the import id, got %v", got.Key)
	}
}
