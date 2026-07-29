package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type fakeWorkspaceSettingsHandler struct {
	platformv1connect.UnimplementedWorkspaceSettingsServiceHandler
	settings   *platformv1.WorkspaceSettings
	lastUpdate *platformv1.UpdateWorkspaceSettingsRequest
	updateErr  error
}

func (f *fakeWorkspaceSettingsHandler) GetWorkspaceSettings(_ context.Context, _ *platformv1.GetWorkspaceSettingsRequest) (*platformv1.GetWorkspaceSettingsResponse, error) {
	return &platformv1.GetWorkspaceSettingsResponse{Settings: f.settings}, nil
}

func (f *fakeWorkspaceSettingsHandler) UpdateWorkspaceSettings(_ context.Context, req *platformv1.UpdateWorkspaceSettingsRequest) (*platformv1.UpdateWorkspaceSettingsResponse, error) {
	f.lastUpdate = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &platformv1.UpdateWorkspaceSettingsResponse{Settings: f.settings}, nil
}

func newWorkspaceSettingsClient(t *testing.T, h *fakeWorkspaceSettingsHandler) *Client {
	t.Helper()
	path, handler := platformv1connect.NewWorkspaceSettingsServiceHandler(h)
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

func strp(s string) *string     { return &s }
func floatp(f float64) *float64 { return &f }

func TestWorkspaceSettings_GetProjection(t *testing.T) {
	h := &fakeWorkspaceSettingsHandler{settings: &platformv1.WorkspaceSettings{
		Key:                  "acme",
		DisplayName:          "Acme",
		EnforceEnabledModels: true,
		PiiRedaction: &platformv1.PiiRedaction{
			Enabled: true,
			Config: &platformv1.PiiRedactionConfig{
				Language:  strp("nl"),
				Entities:  []string{"EMAIL_ADDRESS"},
				OnFailure: strp("passthrough"),
				Threshold: floatp(0.7),
			},
		},
	}}
	c := newWorkspaceSettingsClient(t, h)

	s, err := c.WorkspaceSettings().Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Key != "acme" || s.DisplayName != "Acme" || !s.EnforceEnabledModels {
		t.Fatalf("scalar projection wrong: %+v", s)
	}
	if s.PiiRedaction == nil || !s.PiiRedaction.Enabled || s.PiiRedaction.Config == nil {
		t.Fatalf("pii projection missing: %+v", s.PiiRedaction)
	}
	cfg := s.PiiRedaction.Config
	if cfg.Language == nil || *cfg.Language != "nl" {
		t.Errorf("language = %v", cfg.Language)
	}
	if cfg.OnFailure == nil || *cfg.OnFailure != "passthrough" {
		t.Errorf("on_failure = %v", cfg.OnFailure)
	}
	if cfg.Threshold == nil || *cfg.Threshold != 0.7 {
		t.Errorf("threshold = %v", cfg.Threshold)
	}
	if len(cfg.Entities) != 1 || cfg.Entities[0] != "EMAIL_ADDRESS" {
		t.Errorf("entities = %v", cfg.Entities)
	}
}

func TestWorkspaceSettings_GetOmitsAbsentPii(t *testing.T) {
	h := &fakeWorkspaceSettingsHandler{settings: &platformv1.WorkspaceSettings{Key: "acme", DisplayName: "Acme"}}
	c := newWorkspaceSettingsClient(t, h)

	s, err := c.WorkspaceSettings().Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.PiiRedaction != nil {
		t.Fatalf("expected nil pii_redaction, got %+v", s.PiiRedaction)
	}
}

func TestWorkspaceSettings_UpdateOmitsUnsetFields(t *testing.T) {
	h := &fakeWorkspaceSettingsHandler{settings: &platformv1.WorkspaceSettings{Key: "acme", DisplayName: "Acme"}}
	c := newWorkspaceSettingsClient(t, h)

	if _, err := c.WorkspaceSettings().Update(context.Background(), WorkspaceSettingsUpdateInput{
		DisplayName: strp("Acme"),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := h.lastUpdate
	if got.DisplayName == nil || got.GetDisplayName() != "Acme" {
		t.Errorf("display_name not sent: %v", got.DisplayName)
	}
	if got.EnforceEnabledModels != nil {
		t.Errorf("enforce_enabled_models must be omitted when unset, got %v", got.GetEnforceEnabledModels())
	}
	if got.PiiRedaction != nil {
		t.Errorf("pii_redaction must be omitted when unset, got %+v", got.PiiRedaction)
	}
}

func TestWorkspaceSettings_UpdateSendsFalseEnforce(t *testing.T) {
	h := &fakeWorkspaceSettingsHandler{settings: &platformv1.WorkspaceSettings{Key: "acme"}}
	c := newWorkspaceSettingsClient(t, h)

	no := false
	if _, err := c.WorkspaceSettings().Update(context.Background(), WorkspaceSettingsUpdateInput{
		EnforceEnabledModels: &no,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if h.lastUpdate.EnforceEnabledModels == nil {
		t.Fatal("explicit false was dropped from the request")
	}
	if h.lastUpdate.GetEnforceEnabledModels() {
		t.Fatal("enforce_enabled_models = true on the wire, want false")
	}
}

func TestWorkspaceSettings_UpdateSendsFullPii(t *testing.T) {
	h := &fakeWorkspaceSettingsHandler{settings: &platformv1.WorkspaceSettings{Key: "acme"}}
	c := newWorkspaceSettingsClient(t, h)

	if _, err := c.WorkspaceSettings().Update(context.Background(), WorkspaceSettingsUpdateInput{
		PiiRedaction: &PiiRedaction{
			Enabled: true,
			Config: &PiiRedactionConfig{
				Language: strp("en"),
				Entities: []string{"PERSON"},
				// on_failure / threshold deliberately dropped.
			},
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	pii := h.lastUpdate.GetPiiRedaction()
	if pii == nil || !pii.GetEnabled() {
		t.Fatalf("pii not sent: %+v", pii)
	}
	cfg := pii.GetConfig()
	if cfg == nil || cfg.GetLanguage() != "en" || len(cfg.GetEntities()) != 1 {
		t.Fatalf("pii config not sent: %+v", cfg)
	}
	if cfg.OnFailure != nil || cfg.Threshold != nil {
		t.Errorf("dropped fields leaked onto the wire: on_failure=%v threshold=%v", cfg.OnFailure, cfg.Threshold)
	}
}

// The seam does not paper over the lossy encoding: an empty list is sent as an
// empty list, and the server then stores no entities key at all.
func TestWorkspaceSettings_UpdateEmptyEntities(t *testing.T) {
	h := &fakeWorkspaceSettingsHandler{settings: &platformv1.WorkspaceSettings{Key: "acme"}}
	c := newWorkspaceSettingsClient(t, h)

	if _, err := c.WorkspaceSettings().Update(context.Background(), WorkspaceSettingsUpdateInput{
		PiiRedaction: &PiiRedaction{Enabled: true, Config: &PiiRedactionConfig{Entities: []string{}}},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	cfg := h.lastUpdate.GetPiiRedaction().GetConfig()
	if cfg == nil {
		t.Fatal("config block dropped")
	}
	if len(cfg.GetEntities()) != 0 {
		t.Errorf("entities = %v, want empty", cfg.GetEntities())
	}
}

// `enabled` is a plain proto3 bool with no field presence, so what must survive
// the wire is the enclosing pii_redaction MESSAGE: an omitted block means "leave
// PII redaction alone", `{enabled: false}` means "turn the default off".
func TestWorkspaceSettings_UpdateSendsDisabledPii(t *testing.T) {
	h := &fakeWorkspaceSettingsHandler{settings: &platformv1.WorkspaceSettings{Key: "acme"}}
	c := newWorkspaceSettingsClient(t, h)

	if _, err := c.WorkspaceSettings().Update(context.Background(), WorkspaceSettingsUpdateInput{
		PiiRedaction: &PiiRedaction{Enabled: false},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if h.lastUpdate.PiiRedaction == nil {
		t.Fatal("an explicit enabled=false block was dropped from the request (it is NOT the same as omitting it)")
	}
	if h.lastUpdate.GetPiiRedaction().GetEnabled() {
		t.Error("enabled = true on the wire, want false")
	}
	if h.lastUpdate.GetPiiRedaction().GetConfig() != nil {
		t.Error("no config was set, so none must be sent")
	}
}

// threshold 0 is a MEANINGFUL, legal value ("redact at any confidence"), so a
// pointer to 0 must serialize as an explicit 0.
func TestWorkspaceSettings_UpdateSendsZeroThreshold(t *testing.T) {
	h := &fakeWorkspaceSettingsHandler{settings: &platformv1.WorkspaceSettings{Key: "acme"}}
	c := newWorkspaceSettingsClient(t, h)

	if _, err := c.WorkspaceSettings().Update(context.Background(), WorkspaceSettingsUpdateInput{
		PiiRedaction: &PiiRedaction{Enabled: true, Config: &PiiRedactionConfig{Threshold: floatp(0)}},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	cfg := h.lastUpdate.GetPiiRedaction().GetConfig()
	if cfg == nil {
		t.Fatal("config block dropped")
	}
	if cfg.Threshold == nil {
		t.Fatal("an explicit threshold of 0 was dropped from the request")
	}
	if *cfg.Threshold != 0 {
		t.Errorf("threshold = %v on the wire, want 0", *cfg.Threshold)
	}
}

func TestWorkspaceSettings_ErrorNormalized(t *testing.T) {
	h := &fakeWorkspaceSettingsHandler{
		settings:  &platformv1.WorkspaceSettings{Key: "acme"},
		updateErr: connect.NewError(connect.CodeInvalidArgument, errUnknownEntity),
	}
	c := newWorkspaceSettingsClient(t, h)

	_, err := c.WorkspaceSettings().Update(context.Background(), WorkspaceSettingsUpdateInput{DisplayName: strp("x")})
	if err == nil {
		t.Fatal("expected an error")
	}
	if CodeOf(err) != CodeInvalid {
		t.Errorf("code = %q, want %q", CodeOf(err), CodeInvalid)
	}
}

var errUnknownEntity = &staticError{"unknown entity type"}

type staticError struct{ msg string }

func (e *staticError) Error() string { return e.msg }

func TestWorkspaceSettings_InputIsEmpty(t *testing.T) {
	if !(WorkspaceSettingsUpdateInput{}).IsEmpty() {
		t.Error("zero input must be empty")
	}
	no := false
	if (WorkspaceSettingsUpdateInput{EnforceEnabledModels: &no}).IsEmpty() {
		t.Error("an explicit false is a write, not an empty input")
	}
	if (WorkspaceSettingsUpdateInput{PiiRedaction: &PiiRedaction{}}).IsEmpty() {
		t.Error("a pii block is a write, not an empty input")
	}
}
