package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

func TestMcpGatewayValidateConfig(t *testing.T) {
	ctx := context.Background()
	r := &mcpGatewayResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)

	validate := func(m mcpGatewayResourceModel) bool {
		plan := planFor(t, r, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{
			Config: tfsdk.Config{Schema: sch.Schema, Raw: plan.Raw},
		}, &resp)
		return resp.Diagnostics.HasError()
	}

	link := func(id, alias string, mode string, toolIDs types.List) mcpServerLinkModel {
		return mcpServerLinkModel{
			McpServerID: types.StringValue(id),
			Alias:       types.StringValue(alias),
			Enabled:     types.BoolValue(true),
			ToolExposure: &mcpToolExposureModel{
				Mode:            types.StringValue(mode),
				ReadOnly:        types.BoolValue(false),
				ToolIDs:         toolIDs,
				ExcludedToolIDs: types.ListNull(types.StringType),
			},
		}
	}

	base := mcpGatewayResourceModel{
		Key:         types.StringValue("support"),
		DisplayName: types.StringValue("Support"),
		Mode:        types.StringValue(client.McpGatewayModeDirect),
		ToolNaming:  types.StringValue(client.McpToolNamingPrefixOnCollision),
		ServerLinks: []mcpServerLinkModel{
			link("srv_1", "docs", client.McpExposureModeAll, types.ListNull(types.StringType)),
		},
	}
	if validate(base) {
		t.Error("valid gateway config should pass")
	}

	// Duplicate server id rejected.
	dupServer := base
	dupServer.ServerLinks = []mcpServerLinkModel{
		link("srv_1", "a", client.McpExposureModeAll, types.ListNull(types.StringType)),
		link("srv_1", "b", client.McpExposureModeAll, types.ListNull(types.StringType)),
	}
	if !validate(dupServer) {
		t.Error("duplicate mcp_server_id should fail")
	}

	// Duplicate alias rejected.
	dupAlias := base
	dupAlias.ServerLinks = []mcpServerLinkModel{
		link("srv_1", "docs", client.McpExposureModeAll, types.ListNull(types.StringType)),
		link("srv_2", "docs", client.McpExposureModeAll, types.ListNull(types.StringType)),
	}
	if !validate(dupAlias) {
		t.Error("duplicate alias should fail")
	}

	// tool_ids with mode=ALL rejected.
	badExposure := base
	badExposure.ServerLinks = []mcpServerLinkModel{
		link("srv_1", "docs", client.McpExposureModeAll, stringList("tool_1")),
	}
	if !validate(badExposure) {
		t.Error("tool_ids with mode=ALL should fail")
	}
}
