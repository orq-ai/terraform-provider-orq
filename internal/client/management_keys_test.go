package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	managementkeysv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/managementkeys/v1"
	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type fakeManagementKeysHandler struct {
	platformv1connect.UnimplementedManagementKeysServiceHandler
	lastCreate *platformv1.CreateManagementKeyRequest
	lastUpdate *platformv1.UpdateManagementKeyRequest
	key        *platformv1.ManagementKey
	token      string
}

func (f *fakeManagementKeysHandler) CreateManagementKey(_ context.Context, req *platformv1.CreateManagementKeyRequest) (*platformv1.CreateManagementKeyResponse, error) {
	f.lastCreate = req
	return &platformv1.CreateManagementKeyResponse{ManagementKey: f.key, Token: f.token}, nil
}
func (f *fakeManagementKeysHandler) GetManagementKey(_ context.Context, _ *platformv1.GetManagementKeyRequest) (*platformv1.GetManagementKeyResponse, error) {
	return &platformv1.GetManagementKeyResponse{ManagementKey: f.key}, nil
}
func (f *fakeManagementKeysHandler) UpdateManagementKey(_ context.Context, req *platformv1.UpdateManagementKeyRequest) (*platformv1.UpdateManagementKeyResponse, error) {
	f.lastUpdate = req
	return &platformv1.UpdateManagementKeyResponse{ManagementKey: f.key}, nil
}
func (f *fakeManagementKeysHandler) DeleteManagementKey(_ context.Context, _ *platformv1.DeleteManagementKeyRequest) (*platformv1.DeleteManagementKeyResponse, error) {
	return &platformv1.DeleteManagementKeyResponse{}, nil
}
func (f *fakeManagementKeysHandler) ListManagementKeys(_ context.Context, _ *platformv1.ListManagementKeysRequest) (*platformv1.ListManagementKeysResponse, error) {
	return &platformv1.ListManagementKeysResponse{Object: "list", Data: []*platformv1.ManagementKey{f.key}, HasMore: false}, nil
}

func newManagementKeysClient(t *testing.T, h *fakeManagementKeysHandler) *Client {
	t.Helper()
	path, handler := platformv1connect.NewManagementKeysServiceHandler(h)
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

func sampleManagementKey() *platformv1.ManagementKey {
	return &platformv1.ManagementKey{
		ManagementKeyId: "mk_1",
		Name:            "ci",
		PermissionMode:  platformv1.ManagementPermissionMode_MANAGEMENT_PERMISSION_MODE_RESTRICTED,
		Access: map[string]managementkeysv1.AccessLevel{
			"project": managementkeysv1.AccessLevel_ACCESS_LEVEL_WRITE,
		},
		TokenPrefix: "sk-orq-mk_1",
		Status:      platformv1.ManagementKeyStatus_MANAGEMENT_KEY_STATUS_ACTIVE,
	}
}

func TestManagementKeys_CreateReturnsOneTimeToken(t *testing.T) {
	h := &fakeManagementKeysHandler{key: sampleManagementKey(), token: "sk-orq-mk_1-supersecret"}
	c := newManagementKeysClient(t, h)

	res, err := c.ManagementKeys().Create(context.Background(), ManagementKeyCreateInput{
		Name:           "ci",
		PermissionMode: ManagementPermissionModeRestricted,
		Access:         map[string]string{"project": AccessLevelWrite},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Full-name enums must be wired through exactly.
	if h.lastCreate.GetPermissionMode() != platformv1.ManagementPermissionMode_MANAGEMENT_PERMISSION_MODE_RESTRICTED {
		t.Errorf("permission mode not mapped: %v", h.lastCreate.GetPermissionMode())
	}
	if h.lastCreate.GetAccess()["project"] != managementkeysv1.AccessLevel_ACCESS_LEVEL_WRITE {
		t.Errorf("access not mapped: %v", h.lastCreate.GetAccess())
	}
	// The one-time raw token must be surfaced.
	if res.Token != "sk-orq-mk_1-supersecret" {
		t.Errorf("token not returned: %q", res.Token)
	}
	// Read-back projection uses the full enum names.
	if res.Key.PermissionMode != ManagementPermissionModeRestricted || res.Key.Access["project"] != AccessLevelWrite {
		t.Errorf("read-back wrong: %+v", res.Key)
	}
	if res.Key.Status != "MANAGEMENT_KEY_STATUS_ACTIVE" || res.Key.TokenPrefix != "sk-orq-mk_1" {
		t.Errorf("status/prefix read-back wrong: %+v", res.Key)
	}
}

func TestManagementKeys_UpdateClearsExpiry(t *testing.T) {
	h := &fakeManagementKeysHandler{key: sampleManagementKey()}
	c := newManagementKeysClient(t, h)
	name := "ci2"
	if _, err := c.ManagementKeys().Update(context.Background(), ManagementKeyUpdateInput{
		ID:             "mk_1",
		Name:           &name,
		ClearExpiresAt: true,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !h.lastUpdate.GetClearExpiresAt() {
		t.Errorf("clear_expires_at not set: %v", h.lastUpdate)
	}
	if h.lastUpdate.GetName() != "ci2" {
		t.Errorf("name not sent: %q", h.lastUpdate.GetName())
	}
}

func TestManagementKeys_UnknownEnumIsInvalid(t *testing.T) {
	h := &fakeManagementKeysHandler{key: sampleManagementKey()}
	c := newManagementKeysClient(t, h)
	_, err := c.ManagementKeys().Create(context.Background(), ManagementKeyCreateInput{
		Name:           "x",
		PermissionMode: "NOT_A_MODE",
	})
	if err == nil || CodeOf(err) != CodeInvalid {
		t.Fatalf("expected invalid, got %v", err)
	}
}

func TestManagementKeys_Delete(t *testing.T) {
	h := &fakeManagementKeysHandler{key: sampleManagementKey()}
	c := newManagementKeysClient(t, h)
	if err := c.ManagementKeys().Delete(context.Background(), "mk_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
