package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type fakeNotifiersHandler struct {
	platformv1connect.UnimplementedNotifiersServiceHandler
	lastCreate *platformv1.CreateNotifierRequest
	notifier   *platformv1.Notifier
}

func (f *fakeNotifiersHandler) CreateNotifier(_ context.Context, req *platformv1.CreateNotifierRequest) (*platformv1.CreateNotifierResponse, error) {
	f.lastCreate = req
	return &platformv1.CreateNotifierResponse{Notifier: f.notifier}, nil
}
func (f *fakeNotifiersHandler) GetNotifier(_ context.Context, _ *platformv1.GetNotifierRequest) (*platformv1.GetNotifierResponse, error) {
	return &platformv1.GetNotifierResponse{Notifier: f.notifier}, nil
}
func (f *fakeNotifiersHandler) DeleteNotifier(_ context.Context, _ *platformv1.DeleteNotifierRequest) (*platformv1.DeleteNotifierResponse, error) {
	return &platformv1.DeleteNotifierResponse{}, nil
}

func newNotifiersClient(t *testing.T, h *fakeNotifiersHandler) *Client {
	t.Helper()
	path, handler := platformv1connect.NewNotifiersServiceHandler(h)
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

func TestNotifiers_CreateTypeMapping(t *testing.T) {
	h := &fakeNotifiersHandler{notifier: &platformv1.Notifier{
		Id:          "nf_1",
		DisplayName: "ops",
		Type:        platformv1.NotifierType_NOTIFIER_TYPE_SLACK_WEBHOOK,
	}}
	c := newNotifiersClient(t, h)

	n, err := c.Notifiers().Create(context.Background(), NotifierCreateInput{
		DisplayName:        "ops",
		Type:               NotifierTypeSlackWebhook,
		IncomingWebhookURL: "https://hooks.slack.example/abc",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.lastCreate.GetType() != platformv1.NotifierType_NOTIFIER_TYPE_SLACK_WEBHOOK {
		t.Errorf("type not mapped: %v", h.lastCreate.GetType())
	}
	if h.lastCreate.GetIncomingWebhookUrl() != "https://hooks.slack.example/abc" {
		t.Errorf("webhook url not sent: %q", h.lastCreate.GetIncomingWebhookUrl())
	}
	if n.Type != NotifierTypeSlackWebhook {
		t.Errorf("type read-back = %q, want %q", n.Type, NotifierTypeSlackWebhook)
	}
}

func TestNotifiers_UnknownTypeRejected(t *testing.T) {
	h := &fakeNotifiersHandler{notifier: &platformv1.Notifier{}}
	c := newNotifiersClient(t, h)
	_, err := c.Notifiers().Create(context.Background(), NotifierCreateInput{DisplayName: "x", Type: "PIGEON"})
	if err == nil {
		t.Fatal("expected error for unknown notifier type")
	}
}

func TestNotifiers_Delete(t *testing.T) {
	h := &fakeNotifiersHandler{notifier: &platformv1.Notifier{Id: "nf_1"}}
	c := newNotifiersClient(t, h)
	if err := c.Notifiers().Delete(context.Background(), "nf_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
