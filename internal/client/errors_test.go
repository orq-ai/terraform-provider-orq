package client

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
)

// TestMapRESTTransportError_NoRouteLeak proves a Go *url.Error (which renders as
// `Delete "https://host/v2/…": …`) is not folded into the diagnostic message,
// so the REST route/protocol never crosses the seam — while the raw error stays
// reachable via errors.Is/As.
func TestMapRESTTransportError_NoRouteLeak(t *testing.T) {
	raw := &url.Error{
		Op:  "Delete",
		URL: "https://api.orq.ai/v2/workspace-models/openai/gpt-4o",
		Err: errors.New("connection refused"),
	}
	e := mapRESTTransportError("workspace model", raw)

	if CodeOf(e) != CodeUnavailable {
		t.Errorf("code = %v, want %v", CodeOf(e), CodeUnavailable)
	}
	msg := e.Error()
	for _, leak := range []string{"https://", "/v2/", "Delete", "api.orq.ai"} {
		if strings.Contains(msg, leak) {
			t.Errorf("transport error leaks %q: %q", leak, msg)
		}
	}
	if !strings.Contains(msg, "workspace model") {
		t.Errorf("message should name the domain noun: %q", msg)
	}
	if !errors.Is(e, raw) {
		t.Error("wrapped transport error must stay reachable via errors.Is")
	}
}

// TestMapConnectError_TransportPathNoLeak proves a non-connect (transport-level)
// error on the Connect path is not rendered verbatim (which would embed the RPC
// URL) but replaced with a static per-domain message.
func TestMapConnectError_TransportPathNoLeak(t *testing.T) {
	raw := &url.Error{
		Op:  "Post",
		URL: "https://api.orq.ai/v3/rpc/platform/orq.platform.v1.BudgetsService/CreateBudget",
		Err: errors.New("connection refused"),
	}
	e := mapConnectError("budget", raw)

	if CodeOf(e) != CodeUnavailable {
		t.Errorf("code = %v, want %v", CodeOf(e), CodeUnavailable)
	}
	msg := e.Error()
	for _, leak := range []string{"https://", "/v3/", "BudgetsService", "CreateBudget"} {
		if strings.Contains(msg, leak) {
			t.Errorf("connect transport error leaks %q: %q", leak, msg)
		}
	}
	if !errors.Is(e, raw) {
		t.Error("wrapped transport error must stay reachable via errors.Is")
	}
}

// TestMapConnectError_ServerErrorKeepsMessage proves a genuine connect.Error
// (server response) still surfaces the server-supplied message and mapped code.
func TestMapConnectError_ServerErrorKeepsMessage(t *testing.T) {
	ce := connect.NewError(connect.CodeNotFound, errors.New("budget not found"))
	e := mapConnectError("budget", ce)

	if CodeOf(e) != CodeNotFound {
		t.Errorf("code = %v, want %v", CodeOf(e), CodeNotFound)
	}
	if !strings.Contains(e.Error(), "budget not found") {
		t.Errorf("server message dropped: %q", e.Error())
	}
}
