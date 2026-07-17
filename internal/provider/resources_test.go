package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// TestResourcesRegistered checks all five first-wave resources are registered
// and that each builds its schema and resolves its type name without errors.
func TestResourcesRegistered(t *testing.T) {
	ctx := context.Background()
	p := New("test")()

	factories := p.Resources(ctx)
	if len(factories) != 5 {
		t.Fatalf("expected 5 resources, got %d", len(factories))
	}

	want := map[string]bool{
		"orq_project":         false,
		"orq_budget":          false,
		"orq_notifier":        false,
		"orq_guardrail_rule":  false,
		"orq_workspace_model": false,
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
