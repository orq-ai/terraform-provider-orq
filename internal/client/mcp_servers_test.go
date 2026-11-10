package client

import (
	"slices"
	"testing"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
)

func TestMcpAuthFromProtoPopulatesStaticHeaderNames(t *testing.T) {
	auth := mcpAuthFromProto(&platformv1.McpAuthConfig{
		Type: platformv1.McpAuthType_MCP_AUTH_TYPE_STATIC_HEADERS,
		StaticHeaders: []*platformv1.McpHeaderSecret{
			{Key: "Authorization", MaskedValue: "Bea...xyz"},
			{Key: "X-Tenant", MaskedValue: "abc...123"},
		},
	})
	if auth.Type != McpAuthTypeStaticHeaders {
		t.Fatalf("type = %q, want STATIC_HEADERS", auth.Type)
	}
	if !slices.Equal(auth.StaticHeaderNames, []string{"Authorization", "X-Tenant"}) {
		t.Errorf("StaticHeaderNames = %v, want [Authorization X-Tenant]", auth.StaticHeaderNames)
	}
}
