package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type fakeProjectsCRUD struct {
	platformv1connect.UnimplementedProjectsServiceHandler
	lastCreate *platformv1.CreateProjectRequest
	lastUpdate *platformv1.UpdateProjectRequest
	deleted    string
	project    *platformv1.Project
}

func (f *fakeProjectsCRUD) CreateProject(_ context.Context, req *platformv1.CreateProjectRequest) (*platformv1.CreateProjectResponse, error) {
	f.lastCreate = req
	return &platformv1.CreateProjectResponse{Project: f.project}, nil
}
func (f *fakeProjectsCRUD) GetProject(_ context.Context, _ *platformv1.GetProjectRequest) (*platformv1.GetProjectResponse, error) {
	return &platformv1.GetProjectResponse{Project: f.project}, nil
}
func (f *fakeProjectsCRUD) UpdateProject(_ context.Context, req *platformv1.UpdateProjectRequest) (*platformv1.UpdateProjectResponse, error) {
	f.lastUpdate = req
	return &platformv1.UpdateProjectResponse{Project: f.project}, nil
}
func (f *fakeProjectsCRUD) DeleteProject(_ context.Context, req *platformv1.DeleteProjectRequest) (*platformv1.DeleteProjectResponse, error) {
	f.deleted = req.GetProjectId()
	return &platformv1.DeleteProjectResponse{}, nil
}

func newProjectsCRUDClient(t *testing.T, h *fakeProjectsCRUD) *Client {
	t.Helper()
	path, handler := platformv1connect.NewProjectsServiceHandler(h)
	mux := http.NewServeMux()
	mux.Handle("/v3/rpc/platform"+path, http.StripPrefix("/v3/rpc/platform", handler))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestProjects_CreateGetUpdateDelete(t *testing.T) {
	h := &fakeProjectsCRUD{project: &platformv1.Project{
		ProjectId: "p_1", Name: "alpha", Key: "alpha", Description: "d", Teams: []string{"t1"},
	}}
	c := newProjectsCRUDClient(t, h)
	ctx := context.Background()

	desc := "d"
	p, err := c.Projects().Create(ctx, ProjectCreateInput{Name: "alpha", Description: &desc, Teams: []string{"t1"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.lastCreate.GetName() != "alpha" || h.lastCreate.GetDescription() != "d" {
		t.Errorf("create req wrong: %+v", h.lastCreate)
	}
	if p.ID != "p_1" || len(p.Teams) != 1 {
		t.Errorf("create read-back wrong: %+v", p)
	}

	if _, err := c.Projects().Get(ctx, "p_1"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	name := "beta"
	if _, err := c.Projects().Update(ctx, ProjectUpdateInput{ID: "p_1", Name: &name, Teams: []string{}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if h.lastUpdate.GetName() != "beta" {
		t.Errorf("update name not sent: %v", h.lastUpdate.GetName())
	}

	if err := c.Projects().Delete(ctx, "p_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if h.deleted != "p_1" {
		t.Errorf("delete id = %q, want p_1", h.deleted)
	}
}
