package client

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
)

func TestSanitizeMessage(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"newline and tab collapse to single space", "line1\nline2\tline3", "line1 line2 line3"},
		{"repeated whitespace collapses", "a  \n\t  b", "a b"},
		{"ESC/ANSI control dropped, literal brackets kept", "red\x1b[31mtext\x1b[0m", "red[31mtext[0m"},
		{"C1 control dropped", "ab", "ab"},
		{"surrounding whitespace trimmed", "  \n hello \t ", "hello"},
		{"inert markup left as-is", "line\nline[2J<b>x</b>", "line line[2J<b>x</b>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeMessage(tc.in); got != tc.want {
				t.Errorf("sanitizeMessage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Truncation is by RUNE count (512), never mid-rune, with an ellipsis appended.
	t.Run("truncates by rune count without splitting a rune", func(t *testing.T) {
		in := strings.Repeat("é", 600) // 600 runes, 1200 bytes
		got := sanitizeMessage(in)
		runes := []rune(got)
		if len(runes) != 513 { // 512 kept + 1 ellipsis rune
			t.Fatalf("want 513 runes (512 + ellipsis), got %d", len(runes))
		}
		if runes[512] != '…' {
			t.Errorf("truncated message must end with an ellipsis, got %q", string(runes[512]))
		}
		for i := 0; i < 512; i++ {
			if runes[i] != 'é' {
				t.Fatalf("rune %d altered/split: %q", i, string(runes[i]))
			}
		}
	})
}

// connect-go flags a wire message on protocol SHAPE, so a hostile proxy can
// supply one: an ANSI escape must never reach the diagnostic.
func TestMapConnectError_WireMessageSanitized(t *testing.T) {
	ce := connect.NewWireError(connect.CodeInvalidArgument, errors.New("bad\x1b[2Jinput\nvalue"))
	e := mapConnectError("budget", ce)
	msg := e.Error()
	if strings.ContainsRune(msg, '\x1b') || strings.ContainsRune(msg, '\n') {
		t.Errorf("wire message not sanitized: %q", msg)
	}
	if !strings.Contains(msg, "bad") || !strings.Contains(msg, "input value") {
		t.Errorf("sanitized wire message lost content: %q", msg)
	}
}

// A Go *url.Error renders as `Delete "https://host/v2/…": …`.
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

func TestMapRESTStatus_NoTransportLeak(t *testing.T) {
	body := []byte(`<html><body>500 Internal Server Error: upstream boom</body></html>`)
	e := mapRESTStatus(500, body)

	if CodeOf(e) != CodeInternal {
		t.Errorf("code = %v, want %v", CodeOf(e), CodeInternal)
	}
	msg := e.Error()
	for _, leak := range []string{"HTTP", "500", "<html>", "boom", "Internal Server Error"} {
		if strings.Contains(msg, leak) {
			t.Errorf("rendered message leaks %q: %q", leak, msg)
		}
	}
	// The underlying detail must stay reachable (not rendered, but unwrappable).
	if errors.Unwrap(e) == nil {
		t.Error("wrapped detail must remain reachable via errors.Unwrap")
	}
}

func TestMapRESTStatus_NeutralPhrasePerCode(t *testing.T) {
	e := mapRESTStatus(404, nil)
	if CodeOf(e) != CodeNotFound {
		t.Errorf("code = %v, want %v", CodeOf(e), CodeNotFound)
	}
	if !strings.Contains(e.Error(), "not found") {
		t.Errorf("expected a neutral not-found phrase, got %q", e.Error())
	}
}

// A real server error arrives as a WIRE error, hence NewWireError here.
func TestMapConnectError_ServerErrorKeepsMessage(t *testing.T) {
	ce := connect.NewWireError(connect.CodeNotFound, errors.New("budget not found"))
	e := mapConnectError("budget", ce)

	if CodeOf(e) != CodeNotFound {
		t.Errorf("code = %v, want %v", CodeOf(e), CodeNotFound)
	}
	if !strings.Contains(e.Error(), "budget not found") {
		t.Errorf("server message dropped: %q", e.Error())
	}
}

func TestMapConnectError_NonWireHTTPStatusNeutralized(t *testing.T) {
	// connect-go builds exactly this for an unwrapped HTTP response: NewError (not
	// NewWireError), with the raw status line as the message.
	ce := connect.NewError(connect.CodeUnavailable, errors.New("HTTP status 505 HTTP Version Not Supported"))
	e := mapConnectError("budget", ce)

	if CodeOf(e) != CodeUnavailable {
		t.Errorf("code = %v, want %v", CodeOf(e), CodeUnavailable)
	}
	msg := e.Error()
	for _, leak := range []string{"HTTP status", "505", "HTTP Version Not Supported"} {
		if strings.Contains(msg, leak) {
			t.Errorf("non-wire connect error leaks %q: %q", leak, msg)
		}
	}
	if !strings.Contains(msg, "temporarily unavailable") {
		t.Errorf("expected a neutral unavailable phrase, got %q", msg)
	}
}

func TestMapRESTStatus_SurfacesServerMessage(t *testing.T) {
	t.Run("json envelope surfaced", func(t *testing.T) {
		e := mapRESTStatus(400, []byte(`{"error":"Model validation failed. Please check your configuration."}`))
		if CodeOf(e) != CodeInvalid {
			t.Errorf("code = %v, want %v", CodeOf(e), CodeInvalid)
		}
		if !strings.Contains(e.Error(), "Model validation failed") {
			t.Errorf("server message not surfaced: %q", e.Error())
		}
		// The transport internals still must not leak into the message.
		for _, leak := range []string{"HTTP", "400", "status"} {
			if strings.Contains(e.Error(), leak) {
				t.Errorf("message leaks %q: %q", leak, e.Error())
			}
		}
	})
	t.Run("non-json body falls back to neutral", func(t *testing.T) {
		e := mapRESTStatus(400, []byte(`<html>400 Bad Request: boom</html>`))
		if strings.Contains(e.Error(), "boom") || strings.Contains(e.Error(), "<html>") {
			t.Errorf("HTML body leaked into message: %q", e.Error())
		}
		if !strings.Contains(e.Error(), "rejected as invalid") {
			t.Errorf("expected neutral invalid phrase, got %q", e.Error())
		}
	})
}

func TestServerRESTMessageParsesMessageEnvelope(t *testing.T) {
	if got := serverRESTMessage([]byte(`{"code":"invalid_request_body","message":"Project Default not found in your workspace"}`)); got != "Project Default not found in your workspace" {
		t.Fatalf("message envelope: got %q", got)
	}
	if got := serverRESTMessage([]byte(`{"error":"router says no","message":"ignored"}`)); got != "router says no" {
		t.Fatalf("error precedence: got %q", got)
	}
}
