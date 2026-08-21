package provider

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// TestResourcesRegistered checks all resources are registered and that each
// builds its schema and resolves its type name without errors.
func TestResourcesRegistered(t *testing.T) {
	ctx := context.Background()
	p := New("test")()

	factories := p.Resources(ctx)
	if len(factories) != 12 {
		t.Fatalf("expected 12 resources, got %d", len(factories))
	}

	want := map[string]bool{
		"orq_project":            false,
		"orq_budget":             false,
		"orq_notifier":           false,
		"orq_guardrail_rule":     false,
		"orq_workspace_model":    false,
		"orq_routing_rule":       false,
		"orq_api_key":            false,
		"orq_management_key":     false,
		"orq_model":              false,
		"orq_bedrock_model":      false,
		"orq_workspace_settings": false,
		"orq_evaluator":          false,
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

func stringList(ss ...string) types.List {
	return stringListValue(ss)
}

// --- workspace_model sharing validation + normalization -------------------

func TestValidateSharingAutoGrant(t *testing.T) {
	cases := []struct {
		name       string
		sharing    *workspaceModelSharingModel
		wantDetail string // empty means the config is accepted
	}{
		{
			name: "auto_grant with project_ids rejected",
			sharing: &workspaceModelSharingModel{
				AutoGrantNewProjects: types.BoolValue(true),
				ProjectIDs:           stringList("p1"),
				AllProjects:          types.BoolNull(),
			},
			wantDetail: "cannot be combined with an explicit project_ids list",
		},
		{
			name: "auto_grant with all_projects allowed",
			sharing: &workspaceModelSharingModel{
				AutoGrantNewProjects: types.BoolValue(true),
				ProjectIDs:           types.ListNull(types.StringType),
				AllProjects:          types.BoolValue(true),
			},
		},
		{
			name: "selected without auto_grant allowed",
			sharing: &workspaceModelSharingModel{
				AutoGrantNewProjects: types.BoolValue(false),
				ProjectIDs:           stringList("p1", "p2"),
				AllProjects:          types.BoolNull(),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := validateSharingAutoGrant(tc.sharing)
			if tc.wantDetail == "" {
				if diags.HasError() {
					t.Errorf("this config must be accepted, got %v", diags)
				}
				return
			}
			requireDiagnosticAt(t, diags, "sharing.auto_grant_new_projects", tc.wantDetail)
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

// TestWorkspaceModelApplyModelSetsResolvedID proves applyModel writes the
// resolved DOCUMENT id into `id` while leaving `model_id` (the caller's identity
// value — here a human-readable ref) untouched. Rewriting model_id to the UUID
// would produce a perpetual diff against a config that used the ref.
func TestWorkspaceModelApplyModelSetsResolvedID(t *testing.T) {
	r := &workspaceModelResource{}
	m := workspaceModelResourceModel{
		ModelID: types.StringValue("openai/gpt-4o"), // the user's ref, must be preserved
	}
	r.applyModel(&client.WorkspaceModel{
		ModelID:     "doc_uuid_123", // the catalog document id we read by (resolved UUID)
		DisplayName: "GPT-4o",
		Enabled:     true,
	}, &m)

	if m.ID.ValueString() != "doc_uuid_123" {
		t.Errorf("id must be the resolved document uuid, got %q", m.ID.ValueString())
	}
	if m.ModelID.ValueString() != "openai/gpt-4o" {
		t.Errorf("model_id must stay the user's ref (never rewritten to the uuid), got %q", m.ModelID.ValueString())
	}
	if !m.Enabled.ValueBool() {
		t.Error("enabled must be true")
	}
	if m.DisplayName.ValueString() != "GPT-4o" {
		t.Errorf("display_name not applied: %q", m.DisplayName.ValueString())
	}
}

// --- workspace_model all_projects = false ----------------------------------

func workspaceModelSchema(t *testing.T) schema.Schema {
	t.Helper()
	var sch resource.SchemaResponse
	NewWorkspaceModelResource().Schema(context.Background(), resource.SchemaRequest{}, &sch)
	return sch.Schema
}

func workspaceModelRaw(t *testing.T, s schema.Schema, m workspaceModelResourceModel) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := state.Set(ctx, &m); diags.HasError() {
		t.Fatalf("building value: %v", diags)
	}
	return state.Raw
}

// TestWorkspaceModelAllProjectsOnlyAcceptsTrue drives every validator the schema
// hangs on sharing.all_projects, so it covers both the new true-only rule and the
// ExactlyOneOf pairing it must not displace. all_projects = false used to pass
// validation, be written as selected-with-no-projects, and read back as
// all_projects = null — an inconsistent result that tainted the resource.
func TestWorkspaceModelAllProjectsOnlyAcceptsTrue(t *testing.T) {
	ctx := context.Background()
	sharingAttr := workspaceModelSchema(t).Attributes["sharing"].(schema.SingleNestedAttribute)
	validators := sharingAttr.Attributes["all_projects"].(schema.BoolAttribute).Validators

	cases := []struct {
		name        string
		allProjects types.Bool
		projectIDs  types.List
		wantError   bool
	}{
		{"true accepted", types.BoolValue(true), types.ListNull(types.StringType), false},
		{"false rejected", types.BoolValue(false), types.ListNull(types.StringType), true},
		{"null accepted with project_ids", types.BoolNull(), stringList("p1"), false},
		{"null accepted with an empty project_ids", types.BoolNull(), stringListValue([]string{}), false},
		// The pairing rule still belongs to ExactlyOneOf: neither set is an error.
		{"neither set rejected", types.BoolNull(), types.ListNull(types.StringType), true},
		// Only known at apply — deferred here, caught by the Create/Update pre-flight.
		{"unknown deferred", types.BoolUnknown(), types.ListNull(types.StringType), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := workspaceModelSchema(t)
			raw := workspaceModelRaw(t, s, workspaceModelResourceModel{
				ModelID: types.StringValue("openai/gpt-4o"),
				Sharing: &workspaceModelSharingModel{
					AllProjects: tc.allProjects,
					ProjectIDs:  tc.projectIDs,
				},
			})
			req := validator.BoolRequest{
				Path:           path.Root("sharing").AtName("all_projects"),
				PathExpression: path.MatchRoot("sharing").AtName("all_projects"),
				Config:         tfsdk.Config{Schema: s, Raw: raw},
				ConfigValue:    tc.allProjects,
			}
			var diags diag.Diagnostics
			for _, v := range validators {
				resp := &validator.BoolResponse{}
				v.ValidateBool(ctx, req, resp)
				diags.Append(resp.Diagnostics...)
			}
			if diags.HasError() != tc.wantError {
				t.Fatalf("HasError = %v, want %v (%v)", diags.HasError(), tc.wantError, diags)
			}
			if tc.name != "false rejected" {
				return
			}
			detail := diags.Errors()[0].Detail()
			if !strings.Contains(detail, "all_projects only accepts true") || !strings.Contains(detail, "project_ids = []") {
				t.Errorf("the diagnostic must point at project_ids = [], got %q", detail)
			}
		})
	}
}

// countingWorkspaceModels and countingResolver record every call the resource
// could make, so a pre-flight rejection can be proven to land before any of them.
type countingWorkspaceModels struct {
	enables  int
	disables int
	sharings int
	gets     int
}

func (s *countingWorkspaceModels) Enable(context.Context, string) error { s.enables++; return nil }
func (s *countingWorkspaceModels) Disable(context.Context, string) error {
	s.disables++
	return nil
}
func (s *countingWorkspaceModels) Get(_ context.Context, modelID string) (*client.WorkspaceModel, error) {
	s.gets++
	return &client.WorkspaceModel{
		ModelID: modelID,
		Enabled: true,
		Sharing: &client.SharingConfig{Mode: client.SharingModeAllProjects},
	}, nil
}
func (s *countingWorkspaceModels) SetSharing(context.Context, string, client.SharingInput) error {
	s.sharings++
	return nil
}

type countingResolver struct {
	catalogModels
	resolves int
}

func (c *countingResolver) Resolve(ctx context.Context, ref string) (*client.Model, error) {
	c.resolves++
	return c.catalogModels.Resolve(ctx, ref)
}

// TestWorkspaceModelSharingShapeRejectedBeforeTheWrite covers the shapes the
// plan-time validators DEFER on: every one of them skips an unknown value, and
// the framework never re-runs them at apply, so an interpolation that resolves
// badly reaches the server, comes back normalized, and taints the resource. The
// Create/Update pre-flight re-checks the resolved shape before any call.
func TestWorkspaceModelSharingShapeRejectedBeforeTheWrite(t *testing.T) {
	ctx := context.Background()
	s := workspaceModelSchema(t)

	cases := []struct {
		name    string
		sharing workspaceModelSharingModel
		want    string
	}{
		{
			// The schema validator catches this one too, unless it was interpolated.
			name:    "all_projects resolved to false",
			sharing: workspaceModelSharingModel{AllProjects: types.BoolValue(false), ProjectIDs: types.ListNull(types.StringType)},
			want:    "all_projects only accepts true",
		},
		{
			// sharingInput would pick all-projects and silently drop project_ids.
			name:    "all_projects resolved to true alongside project_ids",
			sharing: workspaceModelSharingModel{AllProjects: types.BoolValue(true), ProjectIDs: stringList("p1")},
			want:    "2 attributes specified",
		},
		{
			// sharingInput would turn the absent list into [], which reads back non-null.
			name:    "project_ids resolved to null with no all_projects",
			sharing: workspaceModelSharingModel{AllProjects: types.BoolNull(), ProjectIDs: types.ListNull(types.StringType)},
			want:    "No attribute specified",
		},
		{
			// The server 400s the sharing write, after the enable already landed.
			name:    "duplicate project_ids",
			sharing: workspaceModelSharingModel{AllProjects: types.BoolNull(), ProjectIDs: stringList("p1", "p1")},
			want:    "must not repeat a project id",
		},
		{
			// stringSlice fails on the null element, also after the enable.
			name: "null project_ids element",
			sharing: workspaceModelSharingModel{
				AllProjects: types.BoolNull(),
				ProjectIDs:  types.ListValueMust(types.StringType, []attr.Value{types.StringValue("p1"), types.StringNull()}),
			},
			want: "must not contain a null element",
		},
		{
			name: "auto_grant resolved to true alongside project_ids",
			sharing: workspaceModelSharingModel{
				AllProjects:          types.BoolNull(),
				ProjectIDs:           stringList("p1"),
				AutoGrantNewProjects: types.BoolValue(true),
			},
			want: "auto_grant_new_projects = true cannot be combined",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sharing := tc.sharing
			sharing.AllowVersionPin = types.BoolValue(false)
			sharing.AllowFork = types.BoolValue(false)
			if sharing.AutoGrantNewProjects.IsNull() {
				sharing.AutoGrantNewProjects = types.BoolValue(false)
			}
			plan := tfsdk.Plan{Schema: s, Raw: workspaceModelRaw(t, s, workspaceModelResourceModel{
				ID:          types.StringValue("doc_uuid_123"),
				ModelID:     types.StringValue("openai/gpt-4o"),
				Enabled:     types.BoolValue(true),
				DisplayName: types.StringNull(),
				Sharing:     &sharing,
			})}
			emptyState := func() tfsdk.State {
				return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
			}
			newResource := func() (*workspaceModelResource, *countingWorkspaceModels, *countingResolver) {
				api := &countingWorkspaceModels{}
				res := &countingResolver{catalogModels: catalogModels{docs: []client.Model{{ID: "doc_uuid_123", RefID: "openai/gpt-4o"}}}}
				return &workspaceModelResource{models: api, resolver: res}, api, res
			}
			check := func(diags diag.Diagnostics, api *countingWorkspaceModels, res *countingResolver) {
				t.Helper()
				if !diags.HasError() {
					t.Fatal("the resolved sharing shape must be rejected")
				}
				var details []string
				for _, e := range diags.Errors() {
					details = append(details, e.Detail())
				}
				if !strings.Contains(strings.Join(details, "\n"), tc.want) {
					t.Errorf("the diagnostic must explain the constraint, got %q", details)
				}
				if res.resolves != 0 || api.enables != 0 || api.disables != 0 || api.sharings != 0 || api.gets != 0 {
					t.Errorf("nothing must be called before the rejection: %d resolves, %d enables, %d disables, %d sharing writes, %d gets",
						res.resolves, api.enables, api.disables, api.sharings, api.gets)
				}
			}

			r, api, res := newResource()
			createResp := resource.CreateResponse{State: emptyState()}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &createResp)
			check(createResp.Diagnostics, api, res)

			// Same guard on update, so an interpolation cannot poison a live grant.
			r, api, res = newResource()
			updateResp := resource.UpdateResponse{State: emptyState()}
			r.Update(ctx, resource.UpdateRequest{Plan: plan, State: emptyState()}, &updateResp)
			check(updateResp.Diagnostics, api, res)
		})
	}
}

// sortingWorkspaceModels mirrors the live backend: it stores the sharing it was
// given and reads the grants back sorted ascending.
type sortingWorkspaceModels struct{ ids []string }

func (s *sortingWorkspaceModels) Enable(context.Context, string) error  { return nil }
func (s *sortingWorkspaceModels) Disable(context.Context, string) error { return nil }
func (s *sortingWorkspaceModels) SetSharing(_ context.Context, _ string, in client.SharingInput) error {
	s.ids = slices.Clone(in.ProjectIDs)
	return nil
}
func (s *sortingWorkspaceModels) Get(_ context.Context, modelID string) (*client.WorkspaceModel, error) {
	ids := slices.Clone(s.ids)
	slices.Sort(ids)
	return &client.WorkspaceModel{
		ModelID:     modelID,
		DisplayName: "GPT-4o",
		Enabled:     true,
		Sharing:     &client.SharingConfig{Mode: client.SharingModeSelected, ProjectIDs: ids},
	}, nil
}

// TestWorkspaceModelKeepsConfiguredProjectIDOrder proves the read-back never
// reorders the operator's project_ids. The server sorts the grants, and a list
// is compared positionally, so adopting that order made every create and update
// of a non-ascending list an inconsistent result that tainted the resource.
func TestWorkspaceModelKeepsConfiguredProjectIDOrder(t *testing.T) {
	ctx := context.Background()
	s := workspaceModelSchema(t)
	api := &sortingWorkspaceModels{}
	r := &workspaceModelResource{
		models:   api,
		resolver: &catalogModels{docs: []client.Model{{ID: "doc_uuid_123", RefID: "openai/gpt-4o"}}},
	}
	descending := []string{"p3", "p2", "p1"}
	model := func(ids types.List) workspaceModelResourceModel {
		return workspaceModelResourceModel{
			ID:          types.StringValue("doc_uuid_123"),
			ModelID:     types.StringValue("openai/gpt-4o"),
			Enabled:     types.BoolValue(true),
			DisplayName: types.StringNull(),
			Sharing: &workspaceModelSharingModel{
				AllProjects:          types.BoolNull(),
				ProjectIDs:           ids,
				AllowVersionPin:      types.BoolValue(false),
				AllowFork:            types.BoolValue(false),
				AutoGrantNewProjects: types.BoolValue(false),
			},
		}
	}
	ids := func(m workspaceModelResourceModel) []string {
		t.Helper()
		got, ok := knownListStrings(m.Sharing.ProjectIDs)
		if !ok {
			t.Fatalf("project_ids not fully known: %v", m.Sharing.ProjectIDs)
		}
		return got
	}
	emptyState := func() tfsdk.State {
		return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	}
	get := func(t *testing.T, from interface {
		Get(context.Context, any) diag.Diagnostics
	}) workspaceModelResourceModel {
		t.Helper()
		var out workspaceModelResourceModel
		if diags := from.Get(ctx, &out); diags.HasError() {
			t.Fatalf("reading state: %v", diags)
		}
		return out
	}

	createResp := resource.CreateResponse{State: emptyState()}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: workspaceModelRaw(t, s, model(stringList(descending...)))},
	}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", createResp.Diagnostics)
	}
	created := get(t, &createResp.State)
	if got := ids(created); !slices.Equal(got, descending) {
		t.Fatalf("create must keep the configured order, got %v", got)
	}

	// A refresh of that state is a fixed point.
	readResp := resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: workspaceModelRaw(t, s, created)}}
	r.Read(ctx, resource.ReadRequest{State: readResp.State}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read: %v", readResp.Diagnostics)
	}
	if got := ids(get(t, &readResp.State)); !slices.Equal(got, descending) {
		t.Fatalf("a refresh must not reorder project_ids, got %v", got)
	}

	// An update reordering the same ids keeps the NEW order, not the stored one.
	reordered := []string{"p1", "p3", "p2"}
	updateResp := resource.UpdateResponse{State: emptyState()}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: workspaceModelRaw(t, s, model(stringList(reordered...)))},
		State: emptyState(),
	}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update: %v", updateResp.Diagnostics)
	}
	if got := ids(get(t, &updateResp.State)); !slices.Equal(got, reordered) {
		t.Fatalf("update must keep the newly configured order, got %v", got)
	}

	// A genuine out-of-band membership change is still reflected, sorted.
	api.ids = []string{"p9", "p1"}
	driftResp := resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: workspaceModelRaw(t, s, created)}}
	r.Read(ctx, resource.ReadRequest{State: driftResp.State}, &driftResp)
	if driftResp.Diagnostics.HasError() {
		t.Fatalf("Read: %v", driftResp.Diagnostics)
	}
	if got := ids(get(t, &driftResp.State)); !slices.Equal(got, []string{"p1", "p9"}) {
		t.Fatalf("a membership change must take the server's grants, got %v", got)
	}
}

// The apply-time guards are a backstop; a literal duplicate or null id must be
// caught by the schema validators, before anything is enabled.
func TestWorkspaceModelProjectIDsRejectedAtPlanTime(t *testing.T) {
	ctx := context.Background()
	sharingAttr := workspaceModelSchema(t).Attributes["sharing"].(schema.SingleNestedAttribute)
	validators := sharingAttr.Attributes["project_ids"].(schema.ListAttribute).Validators

	cases := []struct {
		name       string
		value      types.List
		wantPath   string // empty means the list is accepted
		wantDetail string
	}{
		{name: "unique ids accepted", value: stringList("p1", "p2")},
		{name: "empty list accepted", value: stringList()},
		{
			name: "duplicate rejected", value: stringList("p1", "p1"),
			wantPath: "sharing.project_ids[1]", wantDetail: "must not repeat a project id",
		},
		{
			name:     "null element rejected",
			value:    types.ListValueMust(types.StringType, []attr.Value{types.StringNull()}),
			wantPath: "sharing.project_ids[0]", wantDetail: "must not contain a null element",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var diags diag.Diagnostics
			for _, v := range validators {
				resp := &validator.ListResponse{}
				v.ValidateList(ctx, validator.ListRequest{
					Path:           path.Root("sharing").AtName("project_ids"),
					PathExpression: path.MatchRoot("sharing").AtName("project_ids"),
					ConfigValue:    tc.value,
				}, resp)
				diags.Append(resp.Diagnostics...)
			}
			if tc.wantPath == "" {
				if diags.HasError() {
					t.Errorf("this list must be accepted, got %v", diags)
				}
				return
			}
			requireDiagnosticAt(t, diags, tc.wantPath, tc.wantDetail)
		})
	}
}

// failingSharingWorkspaceModels enables fine but can never write sharing — the
// live failure mode behind a duplicate id or a nonexistent project id. The
// remaining fields choose how the rollback plays out.
type failingSharingWorkspaceModels struct {
	enabled      bool
	disables     int
	disableFails bool // the disable itself errors
	disableNoOps bool // the disable reports success but does not take effect
	getErr       error
}

func (s *failingSharingWorkspaceModels) Enable(context.Context, string) error {
	s.enabled = true
	return nil
}

func (s *failingSharingWorkspaceModels) Disable(context.Context, string) error {
	s.disables++
	if s.disableFails {
		return &client.Error{Code: client.CodeInvalid, Message: "cannot disable"}
	}
	if !s.disableNoOps {
		s.enabled = false
	}
	return nil
}

func (s *failingSharingWorkspaceModels) Get(_ context.Context, modelID string) (*client.WorkspaceModel, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return &client.WorkspaceModel{
		ModelID: modelID,
		Enabled: s.enabled,
		Sharing: &client.SharingConfig{Mode: client.SharingModeAllProjects},
	}, nil
}

func (s *failingSharingWorkspaceModels) SetSharing(context.Context, string, client.SharingInput) error {
	return &client.Error{Code: client.CodeInvalid, Message: "repeated value must contain unique items"}
}

// TestWorkspaceModelRollsBackEnableWhenSharingWriteFails covers the failure the
// plan-time guards cannot see — a nonexistent project id, say. The model is
// already enabled when the sharing write fails, and a bare enabled model
// defaults to all-projects, so a create that stops there leaves the workspace
// fail-open. The enable is undone instead, and the undo is CONFIRMED by a read,
// because a system model id contains a slash and can make the disable no-op
// server-side. Only an undo that cannot be confirmed falls back to persisting
// the tainted partial state.
func TestWorkspaceModelRollsBackEnableWhenSharingWriteFails(t *testing.T) {
	ctx := context.Background()
	s := workspaceModelSchema(t)
	plan := tfsdk.Plan{Schema: s, Raw: workspaceModelRaw(t, s, workspaceModelResourceModel{
		ID:          types.StringNull(),
		ModelID:     types.StringValue("openai/gpt-4o"),
		Enabled:     types.BoolValue(true),
		DisplayName: types.StringNull(),
		Sharing: &workspaceModelSharingModel{
			AllProjects:          types.BoolNull(),
			ProjectIDs:           stringList("p1"),
			AllowVersionPin:      types.BoolValue(false),
			AllowFork:            types.BoolValue(false),
			AutoGrantNewProjects: types.BoolValue(false),
		},
	})}
	create := func(api client.WorkspaceModelsAPI) resource.CreateResponse {
		r := &workspaceModelResource{
			models:   api,
			resolver: &catalogModels{docs: []client.Model{{ID: "doc_uuid_123", RefID: "openai/gpt-4o"}}},
		}
		resp := resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}}
		r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
		return resp
	}

	cases := []struct {
		name      string
		api       *failingSharingWorkspaceModels
		rolledOut bool // the undo was confirmed, so the create left nothing behind
	}{
		{
			name: "the disable takes effect",
			api:  &failingSharingWorkspaceModels{},
			// The read-back confirms the model is gone from the catalog.
			rolledOut: true,
		},
		{
			name:      "the read-back reports the model gone",
			api:       &failingSharingWorkspaceModels{getErr: &client.Error{Code: client.CodeNotFound, Message: "no such model"}},
			rolledOut: true,
		},
		{
			name: "the disable itself fails",
			api:  &failingSharingWorkspaceModels{disableFails: true},
		},
		{
			// A slashed system model id: the disable reports success and does nothing.
			name: "the disable silently no-ops",
			api:  &failingSharingWorkspaceModels{disableNoOps: true},
		},
		{
			name: "the read-back cannot confirm",
			api:  &failingSharingWorkspaceModels{disableNoOps: true, getErr: &client.Error{Code: client.CodeInternal, Message: "boom"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := create(tc.api)
			if !resp.Diagnostics.HasError() {
				t.Fatal("a failed sharing write must fail the create")
			}
			if tc.api.disables != 1 {
				t.Errorf("the enable must be undone exactly once, got %d disables", tc.api.disables)
			}
			detail := resp.Diagnostics.Errors()[0].Detail()

			if tc.rolledOut {
				if !resp.State.Raw.IsNull() {
					t.Error("a confirmed rollback must leave no state")
				}
				if !strings.Contains(detail, "rolled back") {
					t.Errorf("the diagnostic must say the enable was rolled back, got %q", detail)
				}
				return
			}

			// The model may still be enabled, so the partial state is persisted and
			// the resource tainted for the next apply.
			if resp.State.Raw.IsNull() {
				t.Fatal("an unconfirmed rollback must persist a tainted partial state")
			}
			if !strings.Contains(detail, "marked tainted") {
				t.Errorf("the diagnostic must announce the taint, got %q", detail)
			}
			var out workspaceModelResourceModel
			if diags := resp.State.Get(ctx, &out); diags.HasError() {
				t.Fatalf("reading state: %v", diags)
			}
			if !out.Enabled.ValueBool() {
				t.Error("the persisted state must record that the model is enabled")
			}
		})
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
		// Unknown match_cel counts as "possibly present" → defer (no diagnostics),
		// regardless of whether scope is also set; the Create/Update recheck enforces it
		// once known (the framework never re-runs ValidateConfig at apply).
		{"unknown match without scope defers", nil, types.StringUnknown(), false},
		{"unknown match with scope defers", scope, types.StringUnknown(), false},
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

// --- workspace_model import canonicalization ------------------------------

// catalogModels resolves a fixed catalog: by document id, and by ref_id when
// exactly one document carries it (mirroring client.Resolve).
type catalogModels struct{ docs []client.Model }

func (c *catalogModels) Resolve(_ context.Context, ref string) (*client.Model, error) {
	for i := range c.docs {
		if c.docs[i].ID == ref {
			return &c.docs[i], nil
		}
	}
	var matches []*client.Model
	for i := range c.docs {
		if c.docs[i].RefID == ref {
			matches = append(matches, &c.docs[i])
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, &client.Error{Code: client.CodeNotFound, Message: "no such model"}
	default:
		return nil, &client.Error{Code: client.CodeInvalid, Message: "ambiguous reference"}
	}
}

// enabledWorkspaceModels answers every read with the same enabled model.
type enabledWorkspaceModels struct{ gotID string }

func (s *enabledWorkspaceModels) Enable(context.Context, string) error  { return nil }
func (s *enabledWorkspaceModels) Disable(context.Context, string) error { return nil }
func (s *enabledWorkspaceModels) Get(_ context.Context, modelID string) (*client.WorkspaceModel, error) {
	s.gotID = modelID
	return &client.WorkspaceModel{
		ModelID:     modelID,
		DisplayName: "GPT-4o",
		Enabled:     true,
		Sharing:     &client.SharingConfig{Mode: client.SharingModeAllProjects},
	}, nil
}
func (s *enabledWorkspaceModels) SetSharing(context.Context, string, client.SharingInput) error {
	return nil
}

// TestWorkspaceModelImportCanonicalizesDocumentID proves the import read
// rewrites a DOCUMENT id into the human-readable ref. model_id is
// RequiresReplace, so leaving the UUID in state made the first apply against a
// ref-based config silently destroy and recreate the model.
func TestWorkspaceModelImportCanonicalizesDocumentID(t *testing.T) {
	ctx := context.Background()
	docs := []client.Model{{ID: "doc_uuid_123", RefID: "openai/gpt-4o"}}
	models := &enabledWorkspaceModels{}
	r := &workspaceModelResource{models: models, resolver: &catalogModels{docs: docs}}

	var sch resource.SchemaResponse
	NewWorkspaceModelResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	s := sch.Schema

	read := func(prior workspaceModelResourceModel) workspaceModelResourceModel {
		t.Helper()
		state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
		if diags := state.Set(ctx, &prior); diags.HasError() {
			t.Fatalf("seeding state: %v", diags)
		}
		resp := resource.ReadResponse{State: state}
		r.Read(ctx, resource.ReadRequest{State: state}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("Read: %v", resp.Diagnostics)
		}
		var out workspaceModelResourceModel
		if diags := resp.State.Get(ctx, &out); diags.HasError() {
			t.Fatalf("reading state: %v", diags)
		}
		return out
	}

	// Imported by document id: ImportState seeds model_id alone.
	imported := read(workspaceModelResourceModel{
		ID:      types.StringNull(),
		ModelID: types.StringValue("doc_uuid_123"),
	})
	if imported.ModelID.ValueString() != "openai/gpt-4o" {
		t.Errorf("model_id = %q, want the canonical ref", imported.ModelID.ValueString())
	}
	if imported.ID.ValueString() != "doc_uuid_123" {
		t.Errorf("id must stay the document uuid, got %q", imported.ID.ValueString())
	}
	if models.gotID != "doc_uuid_123" {
		t.Errorf("the read must address the document id, got %q", models.gotID)
	}

	// Imported by ref: unchanged.
	byRef := read(workspaceModelResourceModel{
		ID:      types.StringNull(),
		ModelID: types.StringValue("openai/gpt-4o"),
	})
	if byRef.ModelID.ValueString() != "openai/gpt-4o" {
		t.Errorf("a ref import must be left alone, got %q", byRef.ModelID.ValueString())
	}

	// An ordinary refresh (id already in state) never rewrites the operator's
	// own model_id, whichever form they wrote.
	refreshed := read(workspaceModelResourceModel{
		ID:      types.StringValue("doc_uuid_123"),
		ModelID: types.StringValue("doc_uuid_123"),
	})
	if refreshed.ModelID.ValueString() != "doc_uuid_123" {
		t.Errorf("a normal read must preserve model_id, got %q", refreshed.ModelID.ValueString())
	}
}

// A ref shared by two documents cannot name one of them, so the document id has
// to stay: rewriting would make the resource unrecreatable.
func TestWorkspaceModelImportKeepsAmbiguousDocumentID(t *testing.T) {
	ctx := context.Background()
	r := &workspaceModelResource{resolver: &catalogModels{docs: []client.Model{
		{ID: "doc_a", RefID: "openai/gpt-4o"},
		{ID: "doc_b", RefID: "openai/gpt-4o"},
	}}}

	got := r.canonicalModelID(ctx, &client.Model{ID: "doc_a", RefID: "openai/gpt-4o"}, "doc_a")
	if got.ValueString() != "doc_a" {
		t.Errorf("an ambiguous ref must not replace the document id, got %q", got.ValueString())
	}
}
