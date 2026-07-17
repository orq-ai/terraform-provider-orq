package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type fakeSharingHandler struct {
	platformv1connect.UnimplementedModelSharingServiceHandler
	last *platformv1.SetModelSharingRequest
}

func (f *fakeSharingHandler) SetModelSharing(_ context.Context, req *platformv1.SetModelSharingRequest) (*platformv1.SetModelSharingResponse, error) {
	f.last = req
	return &platformv1.SetModelSharingResponse{ModelId: req.GetModelId(), Sharing: req.GetSharing()}, nil
}

// newWorkspaceModelsServer mounts the Connect ModelSharingService plus REST
// routes for enable/disable/list on one server.
func newWorkspaceModelsServer(t *testing.T, models []map[string]any, sh *fakeSharingHandler) (*Client, *[]string) {
	t.Helper()
	shPath, shHandler := platformv1connect.NewModelSharingServiceHandler(sh)
	mux := http.NewServeMux()
	mux.Handle("/v3/rpc/platform"+shPath, http.StripPrefix("/v3/rpc/platform", shHandler))

	var calls []string
	mux.HandleFunc("/v2/workspace-models", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent) // enable
	})
	mux.HandleFunc("/v2/workspace-models/", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent) // disable
	})
	mux.HandleFunc("/v2/models", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, &calls
}

func TestWorkspaceModels_EnableAndSetSharingSelected(t *testing.T) {
	sh := &fakeSharingHandler{}
	c, _ := newWorkspaceModelsServer(t, nil, sh)

	if err := c.WorkspaceModels().Enable(context.Background(), "openai/gpt-4o"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	err := c.WorkspaceModels().SetSharing(context.Background(), "openai/gpt-4o", SharingInput{
		ProjectIDs:      []string{"proj_1", "proj_2"},
		AllowVersionPin: true,
	})
	if err != nil {
		t.Fatalf("SetSharing: %v", err)
	}
	if sh.last.GetModelId() != "openai/gpt-4o" {
		t.Errorf("model id not sent: %q", sh.last.GetModelId())
	}
	sel := sh.last.GetSharing().GetSelected()
	if sel == nil || len(sel.GetProjectIds()) != 2 {
		t.Errorf("selected sharing not wired: %+v", sh.last.GetSharing())
	}
	if !sh.last.GetSharing().GetAllowVersionPin() {
		t.Errorf("allow_version_pin not sent")
	}
}

func TestWorkspaceModels_SetSharingAllProjects(t *testing.T) {
	sh := &fakeSharingHandler{}
	c, _ := newWorkspaceModelsServer(t, nil, sh)
	if err := c.WorkspaceModels().SetSharing(context.Background(), "m1", SharingInput{AllProjects: true}); err != nil {
		t.Fatalf("SetSharing: %v", err)
	}
	if sh.last.GetSharing().GetAllProjects() == nil {
		t.Errorf("all_projects mode not wired: %+v", sh.last.GetSharing())
	}
}

func TestWorkspaceModels_GetNormalizesSelectedEmpty(t *testing.T) {
	// Model shared in "selected" mode with the project_ids key ABSENT — the
	// omitempty wire quirk. Get must normalize it to an empty (non-nil) slice.
	models := []map[string]any{
		{"id": "m1", "display_name": "M1", "enabled": true, "sharing": map[string]any{
			"mode": "selected", "allow_fork": true,
		}},
	}
	c, _ := newWorkspaceModelsServer(t, models, &fakeSharingHandler{})
	wm, err := c.WorkspaceModels().Get(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if wm == nil || !wm.Enabled {
		t.Fatalf("expected enabled model, got %+v", wm)
	}
	if wm.Sharing == nil || wm.Sharing.Mode != SharingModeSelected {
		t.Fatalf("sharing mode wrong: %+v", wm.Sharing)
	}
	if wm.Sharing.ProjectIDs == nil {
		t.Errorf("selected + absent project_ids must normalize to [] (non-nil), got nil")
	}
	if len(wm.Sharing.ProjectIDs) != 0 {
		t.Errorf("expected empty project_ids, got %v", wm.Sharing.ProjectIDs)
	}
	if !wm.Sharing.AllowFork {
		t.Errorf("allow_fork not read back")
	}
}

func TestWorkspaceModels_GetAllProjectsNilIDs(t *testing.T) {
	models := []map[string]any{
		{"id": "m1", "display_name": "M1", "enabled": true, "sharing": map[string]any{"mode": "all_projects"}},
	}
	c, _ := newWorkspaceModelsServer(t, models, &fakeSharingHandler{})
	wm, err := c.WorkspaceModels().Get(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if wm.Sharing.Mode != SharingModeAllProjects || wm.Sharing.ProjectIDs != nil {
		t.Errorf("all_projects should have nil project_ids: %+v", wm.Sharing)
	}
}

func TestWorkspaceModels_GetMissingReturnsNil(t *testing.T) {
	c, _ := newWorkspaceModelsServer(t, []map[string]any{}, &fakeSharingHandler{})
	wm, err := c.WorkspaceModels().Get(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if wm != nil {
		t.Errorf("expected nil for missing model, got %+v", wm)
	}
}

func TestWorkspaceModels_Disable(t *testing.T) {
	c, calls := newWorkspaceModelsServer(t, nil, &fakeSharingHandler{})
	if err := c.WorkspaceModels().Disable(context.Background(), "openai/gpt-4o"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	found := false
	for _, c := range *calls {
		if c == "DELETE /v2/workspace-models/openai/gpt-4o" {
			found = true
		}
	}
	if !found {
		t.Errorf("disable did not DELETE the expected path: %v", *calls)
	}
}
