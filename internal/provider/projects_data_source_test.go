package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// pagingProjects is an in-memory client.ProjectsAPI that paginates a flat list
// by cursor, recording the ListParams of every call.
type pagingProjects struct {
	all      []client.Project
	pageSize int
	gotCalls []client.ListParams
}

func (f *pagingProjects) List(_ context.Context, p client.ListParams) (*client.ProjectPage, error) {
	f.gotCalls = append(f.gotCalls, p)
	start := 0
	if p.StartingAfter != "" {
		for i, pr := range f.all {
			if pr.ID == p.StartingAfter {
				start = i + 1
				break
			}
		}
	}
	end := start + f.pageSize
	if end > len(f.all) {
		end = len(f.all)
	}
	return &client.ProjectPage{Projects: f.all[start:end], HasMore: end < len(f.all)}, nil
}

// brokenProjects always claims has_more with the given page, to exercise the
// infinite-loop guards.
type brokenProjects struct {
	page *client.ProjectPage
}

func (f *brokenProjects) List(_ context.Context, _ client.ListParams) (*client.ProjectPage, error) {
	return f.page, nil
}

// errProjects returns a normalized transport error.
type errProjects struct{ err error }

func (f *errProjects) List(_ context.Context, _ client.ListParams) (*client.ProjectPage, error) {
	return nil, f.err
}

// runRead drives the data source Read against a fake and returns the resulting
// state model + diagnostics.
func runRead(t *testing.T, api client.ProjectsAPI) (projectsDataSourceModel, tfsdk.State, *datasource.ReadResponse) {
	t.Helper()
	ctx := context.Background()
	d := &projectsDataSource{projects: api}

	var sch datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sch)

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch.Schema}}
	d.Read(ctx, datasource.ReadRequest{}, resp)

	var out projectsDataSourceModel
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Get(ctx, &out)...)
	}
	return out, resp.State, resp
}

func makeProjects(n int) []client.Project {
	ps := make([]client.Project, n)
	for i := range ps {
		ps[i] = client.Project{ID: fmt.Sprintf("p_%03d", i), Name: fmt.Sprintf("proj-%d", i), Key: fmt.Sprintf("k%d", i)}
	}
	return ps
}

func TestRead_PaginatesAllPages(t *testing.T) {
	// 450 projects over a 200-page limit => 3 pages (200, 200, 50).
	fake := &pagingProjects{all: makeProjects(450), pageSize: projectsPageLimit}
	out, _, resp := runRead(t, fake)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if len(out.Projects) != 450 {
		t.Fatalf("collected %d projects, want 450", len(out.Projects))
	}
	if len(fake.gotCalls) != 3 {
		t.Fatalf("made %d list calls, want 3", len(fake.gotCalls))
	}
	// Every call must request the 200 page limit, and the cursor must advance.
	for i, c := range fake.gotCalls {
		if c.Limit != projectsPageLimit {
			t.Errorf("call %d limit = %d, want %d", i, c.Limit, projectsPageLimit)
		}
	}
	if fake.gotCalls[0].StartingAfter != "" {
		t.Errorf("first call cursor = %q, want empty", fake.gotCalls[0].StartingAfter)
	}
	if fake.gotCalls[1].StartingAfter != "p_199" || fake.gotCalls[2].StartingAfter != "p_399" {
		t.Errorf("cursors did not advance as expected: %q, %q", fake.gotCalls[1].StartingAfter, fake.gotCalls[2].StartingAfter)
	}
	// Order preserved end-to-end.
	if out.Projects[0].ID.ValueString() != "p_000" || out.Projects[449].ID.ValueString() != "p_449" {
		t.Errorf("ordering broken: first=%s last=%s", out.Projects[0].ID.ValueString(), out.Projects[449].ID.ValueString())
	}
}

func TestRead_EmptyIsNonNullList(t *testing.T) {
	fake := &pagingProjects{all: nil, pageSize: projectsPageLimit}
	out, state, resp := runRead(t, fake)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if len(out.Projects) != 0 {
		t.Fatalf("want 0 projects, got %d", len(out.Projects))
	}
	// The crucial assertion: an empty result is a KNOWN EMPTY list, not null.
	var list types.List
	if diags := state.GetAttribute(context.Background(), path.Root("projects"), &list); diags.HasError() {
		t.Fatalf("GetAttribute: %v", diags)
	}
	if list.IsNull() {
		t.Fatal("projects attribute is null; want an empty (non-null) list")
	}
}

func TestRead_GuardsAgainstInfiniteLoop(t *testing.T) {
	// has_more=true with an empty page.
	emptyButMore := &brokenProjects{page: &client.ProjectPage{Projects: nil, HasMore: true}}
	if _, _, resp := runRead(t, emptyButMore); !resp.Diagnostics.HasError() {
		t.Error("empty-page-with-has_more did not error")
	}

	// has_more=true but cursor never advances (same last id forever).
	stuck := &brokenProjects{page: &client.ProjectPage{
		Projects: []client.Project{{ID: "p_same"}},
		HasMore:  true,
	}}
	if _, _, resp := runRead(t, stuck); !resp.Diagnostics.HasError() {
		t.Error("non-advancing cursor did not error")
	}
}

func TestRead_NormalizedErrorSurfaced(t *testing.T) {
	permErr := &client.Error{Code: client.CodePermissionDenied, Message: "management key lacks project.list"}
	out, _, resp := runRead(t, &errProjects{err: permErr})
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected diagnostics on error")
	}
	if len(out.Projects) != 0 {
		t.Errorf("state should be empty on error, got %d", len(out.Projects))
	}
	// The normalized code must appear; no transport route name may leak.
	detail := resp.Diagnostics.Errors()[0].Detail()
	if !containsAll(detail, string(client.CodePermissionDenied)) {
		t.Errorf("diagnostic missing normalized code: %q", detail)
	}
	if errors.Is(permErr, permErr) && containsAll(detail, "ProjectsService") {
		t.Errorf("diagnostic leaks transport name: %q", detail)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
