package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// TestResourcesRegistered checks all resources are registered and that each
// builds its schema and resolves its type name without errors.
func TestResourcesRegistered(t *testing.T) {
	ctx := context.Background()
	p := New("test")()

	factories := p.Resources(ctx)
	if len(factories) != 9 {
		t.Fatalf("expected 9 resources, got %d", len(factories))
	}

	want := map[string]bool{
		"orq_project":         false,
		"orq_budget":          false,
		"orq_notifier":        false,
		"orq_guardrail_rule":  false,
		"orq_workspace_model": false,
		"orq_routing_rule":    false,
		"orq_policy":          false,
		"orq_api_key":         false,
		"orq_management_key":  false,
	}
	for _, f := range factories {
		r := f()

		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "orq"}, &meta)
		if _, ok := want[meta.TypeName]; !ok {
			t.Errorf("unexpected resource type name %q", meta.TypeName)
			continue
		}
		want[meta.TypeName] = true

		var sch resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sch)
		if sch.Diagnostics.HasError() {
			t.Errorf("%s schema has errors: %v", meta.TypeName, sch.Diagnostics)
		}

		// Every resource must implement import.
		if _, ok := r.(resource.ResourceWithImportState); !ok {
			t.Errorf("%s does not implement ImportState", meta.TypeName)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("resource %q was not registered", name)
		}
	}
}

func boolList(ss ...string) types.List {
	return stringListValue(ss)
}

// --- workspace_model sharing validation + normalization -------------------

func TestValidateSharingAutoGrant(t *testing.T) {
	cases := []struct {
		name      string
		sharing   *workspaceModelSharingModel
		wantError bool
	}{
		{
			name: "auto_grant with project_ids rejected",
			sharing: &workspaceModelSharingModel{
				AutoGrantNewProjects: types.BoolValue(true),
				ProjectIDs:           boolList("p1"),
				AllProjects:          types.BoolNull(),
			},
			wantError: true,
		},
		{
			name: "auto_grant with all_projects allowed",
			sharing: &workspaceModelSharingModel{
				AutoGrantNewProjects: types.BoolValue(true),
				ProjectIDs:           types.ListNull(types.StringType),
				AllProjects:          types.BoolValue(true),
			},
			wantError: false,
		},
		{
			name: "selected without auto_grant allowed",
			sharing: &workspaceModelSharingModel{
				AutoGrantNewProjects: types.BoolValue(false),
				ProjectIDs:           boolList("p1", "p2"),
				AllProjects:          types.BoolNull(),
			},
			wantError: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := validateSharingAutoGrant(tc.sharing)
			if diags.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v (%v)", diags.HasError(), tc.wantError, diags)
			}
		})
	}
}

func TestApplySharingNormalization(t *testing.T) {
	// selected + empty ids => [] (non-null), all_projects null.
	var m workspaceModelResourceModel
	applySharing(&client.SharingConfig{Mode: client.SharingModeSelected, ProjectIDs: []string{}}, &m)
	if m.Sharing == nil {
		t.Fatal("sharing nil")
	}
	if m.Sharing.ProjectIDs.IsNull() {
		t.Error("selected + empty must be a non-null empty list")
	}
	if len(m.Sharing.ProjectIDs.Elements()) != 0 {
		t.Errorf("expected empty list, got %d elems", len(m.Sharing.ProjectIDs.Elements()))
	}
	if !m.Sharing.AllProjects.IsNull() {
		t.Error("selected mode must leave all_projects null")
	}

	// all_projects => project_ids null, all_projects true.
	var m2 workspaceModelResourceModel
	applySharing(&client.SharingConfig{Mode: client.SharingModeAllProjects}, &m2)
	if !m2.Sharing.ProjectIDs.IsNull() {
		t.Error("all_projects must have null project_ids")
	}
	if !m2.Sharing.AllProjects.ValueBool() {
		t.Error("all_projects must be true")
	}
}

// --- budget scope XOR -----------------------------------------------------

func TestValidateBudgetScopeXOR(t *testing.T) {
	scope := &budgetScopeModel{Kind: types.StringValue(client.BudgetScopeWorkspace), Target: types.StringNull()}
	cases := []struct {
		name      string
		scope     *budgetScopeModel
		match     types.String
		wantError bool
	}{
		{"scope only ok", scope, types.StringNull(), false},
		{"match only ok", nil, types.StringValue(`provider == "openai"`), false},
		{"both set rejected", scope, types.StringValue("x"), true},
		{"neither set rejected", nil, types.StringNull(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := validateBudgetScopeXOR(tc.scope, tc.match)
			if diags.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v", diags.HasError(), tc.wantError)
			}
		})
	}
}

// --- budget alert id threading (M2) ---------------------------------------

// TestBudgetWriteInputCarriesAlertID proves an existing alert id is threaded
// into the write so the server edits the alert in place instead of re-minting
// its id, while a new (unknown) alert still maps to an empty id.
func TestBudgetWriteInputCarriesAlertID(t *testing.T) {
	r := &budgetResource{}
	mk := func(id types.String) *budgetResourceModel {
		return &budgetResourceModel{
			Limits: &budgetLimitsModel{Amount: types.Float64Value(100)},
			Alerts: []budgetAlertModel{{
				ID:               id,
				ThresholdPercent: types.Int64Value(80),
				NotifierIDs:      stringListValue([]string{"nf_1"}),
				Dimension:        types.StringNull(),
			}},
		}
	}

	in, err := r.writeInput(context.Background(), mk(types.StringValue("al_1")))
	if err != nil {
		t.Fatalf("writeInput: %v", err)
	}
	if len(in.Alerts) != 1 || in.Alerts[0].ID != "al_1" {
		t.Errorf("existing alert id not threaded: %+v", in.Alerts)
	}

	in2, err := r.writeInput(context.Background(), mk(types.StringUnknown()))
	if err != nil {
		t.Fatalf("writeInput: %v", err)
	}
	if in2.Alerts[0].ID != "" {
		t.Errorf("a new (unknown) alert id must map to empty, got %q", in2.Alerts[0].ID)
	}
}

// --- budget expires_at instant convergence (M3) ---------------------------

// TestBudgetExpiresAtInstantConverges proves a non-UTC config value and the
// server's normalized UTC read-back are semantically equal (no perpetual diff),
// while genuinely different instants are not.
func TestBudgetExpiresAtInstantConverges(t *testing.T) {
	ctx := context.Background()
	config := rfc3339InstantValue("2026-01-01T00:00:00+01:00")
	normalized := rfc3339InstantValue("2025-12-31T23:00:00Z") // same instant, UTC

	eq, diags := config.StringSemanticEquals(ctx, normalized)
	if diags.HasError() {
		t.Fatalf("semantic-equality diags: %v", diags)
	}
	if !eq {
		t.Errorf("config %q and normalized %q must be semantically equal",
			config.ValueString(), normalized.ValueString())
	}

	different := rfc3339InstantValue("2025-12-31T22:00:00Z")
	if neq, _ := config.StringSemanticEquals(ctx, different); neq {
		t.Error("genuinely different instants must not be semantically equal")
	}
}

// TestBudgetApplyExpiresAtConverges proves apply() stores the server's
// normalized value and that it converges with the operator's non-UTC config.
func TestBudgetApplyExpiresAtConverges(t *testing.T) {
	ctx := context.Background()
	r := &budgetResource{}
	m := &budgetResourceModel{ExpiresAt: rfc3339InstantValue("2026-01-01T00:00:00+01:00")}
	config := m.ExpiresAt

	// Server echoes the same instant normalized to UTC.
	r.apply(&client.Budget{ID: "b1", ExpiresAt: "2025-12-31T23:00:00Z"}, m)

	eq, _ := config.StringSemanticEquals(ctx, m.ExpiresAt)
	if !eq {
		t.Errorf("read-back %q does not converge with config %q", m.ExpiresAt.ValueString(), config.ValueString())
	}
}

// --- guardrail options round-trip through the model (H2) ------------------

// TestGuardrailOptionsModelRoundTrip proves the provider decodes an options
// JSON string to the client map on write and re-encodes it on read such that
// the value survives and stays semantically stable.
func TestGuardrailOptionsModelRoundTrip(t *testing.T) {
	models := []guardrailRefModel{{
		ID:        types.StringValue("guard_1"),
		ExecuteOn: types.StringValue("input"),
		Options:   jsontypes.NewNormalizedValue(`{"language":"en","threshold":0.8}`),
	}}

	refs, diags := guardrailRefsFromModel(models)
	if diags.HasError() {
		t.Fatalf("guardrailRefsFromModel: %v", diags)
	}
	if len(refs) != 1 || refs[0].Options == nil || refs[0].Options["language"] != "en" {
		t.Fatalf("options not decoded to map: %+v", refs)
	}

	// Encode the server-shaped ref back into the model and confirm the options
	// survive and are semantically equal to the original config.
	var back guardrailRuleResourceModel
	res := &guardrailRuleResource{}
	res.apply(&client.GuardrailRule{
		ID:         "gr_1",
		Guardrails: refs,
	}, &back)
	if len(back.Guardrails) != 1 || back.Guardrails[0].Options.IsNull() {
		t.Fatalf("options dropped on read-back: %+v", back.Guardrails)
	}
	eq, _ := models[0].Options.StringSemanticEquals(context.Background(), back.Guardrails[0].Options)
	if !eq {
		t.Errorf("options did not round-trip: in=%s out=%s",
			models[0].Options.ValueString(), back.Guardrails[0].Options.ValueString())
	}
}
