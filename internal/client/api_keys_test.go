package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	apikeysv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/apikeys/v1"
	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type fakeAPIKeysHandler struct {
	platformv1connect.UnimplementedApiKeysServiceHandler
	lastCreate *platformv1.CreateApiKeyRequest
	lastUpdate *platformv1.UpdateApiKeyRequest
	key        *platformv1.ApiKey
	token      string
}

func (f *fakeAPIKeysHandler) CreateApiKey(_ context.Context, req *platformv1.CreateApiKeyRequest) (*platformv1.CreateApiKeyResponse, error) {
	f.lastCreate = req
	return &platformv1.CreateApiKeyResponse{ApiKey: f.key, Token: f.token}, nil
}
func (f *fakeAPIKeysHandler) GetApiKey(_ context.Context, _ *platformv1.GetApiKeyRequest) (*platformv1.GetApiKeyResponse, error) {
	return &platformv1.GetApiKeyResponse{ApiKey: f.key}, nil
}
func (f *fakeAPIKeysHandler) UpdateApiKey(_ context.Context, req *platformv1.UpdateApiKeyRequest) (*platformv1.UpdateApiKeyResponse, error) {
	f.lastUpdate = req
	return &platformv1.UpdateApiKeyResponse{ApiKey: f.key}, nil
}
func (f *fakeAPIKeysHandler) DeleteApiKey(_ context.Context, _ *platformv1.DeleteApiKeyRequest) (*platformv1.DeleteApiKeyResponse, error) {
	return &platformv1.DeleteApiKeyResponse{}, nil
}
func (f *fakeAPIKeysHandler) ListApiKeys(_ context.Context, _ *platformv1.ListApiKeysRequest) (*platformv1.ListApiKeysResponse, error) {
	return &platformv1.ListApiKeysResponse{Object: "list", Data: []*platformv1.ApiKey{f.key}, HasMore: false}, nil
}

func newAPIKeysClient(t *testing.T, h *fakeAPIKeysHandler) *Client {
	t.Helper()
	path, handler := platformv1connect.NewApiKeysServiceHandler(h)
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

func sampleAPIKeySingleProject() *platformv1.ApiKey {
	return &platformv1.ApiKey{
		ApiKeyId:       "ak_1",
		Name:           "svc",
		PermissionMode: platformv1.PermissionMode_PERMISSION_MODE_RESTRICTED,
		ProjectScope: &platformv1.ProjectScope{
			Kind: &platformv1.ProjectScope_Single{Single: &platformv1.SingleProject{ProjectId: "proj_9"}},
		},
		Access:      map[string]apikeysv1.AccessLevel{"agent": apikeysv1.AccessLevel_ACCESS_LEVEL_READ},
		TokenPrefix: "sk-orq-ak_1",
		Status:      platformv1.ApiKeyStatus_API_KEY_STATUS_ACTIVE,
	}
}

func TestAPIKeys_CreateSingleProjectScopeAndToken(t *testing.T) {
	h := &fakeAPIKeysHandler{key: sampleAPIKeySingleProject(), token: "sk-orq-ak_1-secret"}
	c := newAPIKeysClient(t, h)

	res, err := c.APIKeys().Create(context.Background(), APIKeyCreateInput{
		Name:           "svc",
		ProjectID:      "proj_9",
		PermissionMode: PermissionModeRestricted,
		Access:         map[string]string{"agent": AccessLevelRead},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A single-project scope must be wired through the oneof.
	if h.lastCreate.GetProjectScope().GetSingle().GetProjectId() != "proj_9" {
		t.Errorf("single-project scope not mapped: %+v", h.lastCreate.GetProjectScope())
	}
	if h.lastCreate.GetPermissionMode() != platformv1.PermissionMode_PERMISSION_MODE_RESTRICTED {
		t.Errorf("permission mode not mapped: %v", h.lastCreate.GetPermissionMode())
	}
	if res.Token != "sk-orq-ak_1-secret" {
		t.Errorf("one-time token not returned: %q", res.Token)
	}
	if res.Key.AllProjects || res.Key.ProjectID != "proj_9" {
		t.Errorf("scope read-back wrong: all=%v id=%q", res.Key.AllProjects, res.Key.ProjectID)
	}
	if res.Key.PermissionMode != PermissionModeRestricted || res.Key.Access["agent"] != AccessLevelRead {
		t.Errorf("read-back wrong: %+v", res.Key)
	}
}

func TestAPIKeys_CreateAllProjectsWhenNoProjectID(t *testing.T) {
	all := &platformv1.ApiKey{
		ApiKeyId:       "ak_2",
		Name:           "all",
		PermissionMode: platformv1.PermissionMode_PERMISSION_MODE_ALL,
		ProjectScope:   &platformv1.ProjectScope{Kind: &platformv1.ProjectScope_All{All: &platformv1.AllProjects{}}},
		TokenPrefix:    "sk-orq-ak_2",
		Status:         platformv1.ApiKeyStatus_API_KEY_STATUS_ACTIVE,
	}
	h := &fakeAPIKeysHandler{key: all, token: "tok"}
	c := newAPIKeysClient(t, h)

	res, err := c.APIKeys().Create(context.Background(), APIKeyCreateInput{Name: "all"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No project id => the provider omits project_scope (server default all-projects).
	if h.lastCreate.GetProjectScope() != nil {
		t.Errorf("project_scope should be omitted for all-projects, got %+v", h.lastCreate.GetProjectScope())
	}
	if !res.Key.AllProjects || res.Key.ProjectID != "" {
		t.Errorf("all-projects read-back wrong: %+v", res.Key)
	}
}

func TestAPIKeys_UpdateReconcilesScope(t *testing.T) {
	h := &fakeAPIKeysHandler{key: sampleAPIKeySingleProject()}
	c := newAPIKeysClient(t, h)
	name := "svc2"
	mode := PermissionModeReadOnly
	if _, err := c.APIKeys().Update(context.Background(), APIKeyUpdateInput{
		ID:              "ak_1",
		Name:            &name,
		PermissionMode:  &mode,
		SetProjectScope: true,
		ProjectID:       "",
		ClearExpiresAt:  true,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// An empty ProjectID with SetProjectScope reconciles to all-projects.
	if h.lastUpdate.GetProjectScope().GetAll() == nil {
		t.Errorf("expected all-projects scope on update, got %+v", h.lastUpdate.GetProjectScope())
	}
	if !h.lastUpdate.GetClearExpiresAt() {
		t.Errorf("clear_expires_at not set")
	}
}

func TestAPIKeys_UnknownEnumIsInvalid(t *testing.T) {
	h := &fakeAPIKeysHandler{key: sampleAPIKeySingleProject()}
	c := newAPIKeysClient(t, h)
	_, err := c.APIKeys().Create(context.Background(), APIKeyCreateInput{Name: "x", Access: map[string]string{"agent": "NOPE"}})
	if err == nil || CodeOf(err) != CodeInvalid {
		t.Fatalf("expected invalid, got %v", err)
	}
}
