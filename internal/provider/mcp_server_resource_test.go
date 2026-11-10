package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

func TestMcpAuthModelFromClientReconcilesAndClearsStaticHeaders(t *testing.T) {
	prior := &mcpAuthModel{
		Type: types.StringValue(client.McpAuthTypeStaticHeaders),
		StaticHeaders: types.MapValueMust(types.StringType, map[string]attr.Value{
			"Authorization": types.StringValue("Bearer secret"),
			"X-Removed":     types.StringValue("old"),
		}),
	}

	// Out-of-band auth type change to NONE must clear static_headers.
	cleared := mcpAuthModelFromClient(prior, client.McpAuth{Type: client.McpAuthTypeNone})
	if !cleared.StaticHeaders.IsNull() {
		t.Errorf("expected null static_headers when auth type is NONE, got %v", cleared.StaticHeaders)
	}

	// Reconcile against server keys: retain Authorization, drop X-Removed, surface X-Added as "".
	reconciled := mcpAuthModelFromClient(prior, client.McpAuth{
		Type:              client.McpAuthTypeStaticHeaders,
		StaticHeaderNames: []string{"Authorization", "X-Added"},
	})
	elems := reconciled.StaticHeaders.Elements()
	if len(elems) != 2 {
		t.Fatalf("expected 2 headers, got %v", elems)
	}
	if elems["Authorization"].(types.String).ValueString() != "Bearer secret" {
		t.Errorf("Authorization should be retained, got %v", elems["Authorization"])
	}
	if elems["X-Added"].(types.String).ValueString() != "" {
		t.Errorf("out-of-band added key should surface as empty string, got %v", elems["X-Added"])
	}
	if _, ok := elems["X-Removed"]; ok {
		t.Error("out-of-band removed key should be dropped from state")
	}
}

func TestMcpServerValidateConfig(t *testing.T) {
	ctx := context.Background()
	r := &mcpServerResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)

	validate := func(m mcpServerResourceModel) bool {
		plan := planFor(t, r, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{
			Config: tfsdk.Config{Schema: sch.Schema, Raw: plan.Raw},
		}, &resp)
		return resp.Diagnostics.HasError()
	}

	base := mcpServerResourceModel{
		Key:         types.StringValue("docs"),
		DisplayName: types.StringValue("Docs"),
		Upstream: &mcpUpstreamModel{
			Type: types.StringValue(client.McpConnectionTypeHTTP),
			URL:  types.StringValue("https://mcp.example.com/mcp"),
		},
		Auth: &mcpAuthModel{
			Type:          types.StringValue(client.McpAuthTypeNone),
			StaticHeaders: types.MapNull(types.StringType),
		},
		DefaultToolExposure: &mcpToolExposureModel{
			Mode:            types.StringValue(client.McpExposureModeAll),
			ReadOnly:        types.BoolValue(false),
			ToolIDs:         types.ListNull(types.StringType),
			ExcludedToolIDs: types.ListNull(types.StringType),
		},
	}
	if validate(base) {
		t.Error("valid HTTP server should pass")
	}

	// OPENAPI without openapi block must fail (prevents inconsistent apply).
	openAPIMissing := base
	openAPIMissing.Upstream = &mcpUpstreamModel{
		Type: types.StringValue(client.McpConnectionTypeOpenAPI),
		URL:  types.StringValue("https://api.example.com"),
	}
	if !validate(openAPIMissing) {
		t.Error("OPENAPI server without openapi block should fail")
	}

	// STATIC_HEADERS without headers must fail.
	staticMissing := base
	staticMissing.Auth = &mcpAuthModel{
		Type:          types.StringValue(client.McpAuthTypeStaticHeaders),
		StaticHeaders: types.MapNull(types.StringType),
	}
	if !validate(staticMissing) {
		t.Error("STATIC_HEADERS without static_headers should fail")
	}

	// Non-http(s) URL must fail.
	badURL := base
	badURL.Upstream = &mcpUpstreamModel{
		Type: types.StringValue(client.McpConnectionTypeHTTP),
		URL:  types.StringValue("ftp://example.com/mcp"),
	}
	if !validate(badURL) {
		t.Error("non-http(s) upstream url should fail")
	}
}
