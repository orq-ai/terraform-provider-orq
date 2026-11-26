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

func TestTelemetryDestinationValidateConfig(t *testing.T) {
	ctx := context.Background()
	r := &telemetryDestinationResource{}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)

	validate := func(m telemetryDestinationResourceModel) bool {
		plan := planFor(t, r, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{
			Config: tfsdk.Config{Schema: sch.Schema, Raw: plan.Raw},
		}, &resp)
		return resp.Diagnostics.HasError()
	}

	base := telemetryDestinationResourceModel{
		EndpointURL:    types.StringValue("https://otel.example.com/v1/traces"),
		ContentMode:    types.StringValue(client.TelemetryContentModeMetadataOnly),
		ProjectScope:   types.StringValue(client.TelemetryProjectScopeAll),
		ProjectIDs:     types.ListNull(types.StringType),
		RequestHeaders: types.MapNull(types.StringType),
		Enabled:        types.BoolValue(true),
	}
	if validate(base) {
		t.Error("valid ALL config should pass")
	}

	withAllProjects := base
	withAllProjects.ProjectIDs = stringList("proj_1")
	if !validate(withAllProjects) {
		t.Error("ALL with project_ids should fail")
	}

	selectedEmpty := base
	selectedEmpty.ProjectScope = types.StringValue(client.TelemetryProjectScopeSelected)
	if !validate(selectedEmpty) {
		t.Error("SELECTED without project_ids should fail")
	}

	dupHeaders := base
	dupHeaders.RequestHeaders = types.MapValueMust(types.StringType, map[string]attr.Value{
		"Authorization": types.StringValue("a"),
		"authorization": types.StringValue("b"),
	})
	if !validate(dupHeaders) {
		t.Error("case-insensitive duplicate request_headers should fail")
	}

	nullHeader := base
	nullHeader.RequestHeaders = types.MapValueMust(types.StringType, map[string]attr.Value{
		"X-Test": types.StringNull(),
	})
	if !validate(nullHeader) {
		t.Error("null request_headers element should fail")
	}
}

func TestTelemetryDestinationRejectsBlankDisplayName(t *testing.T) {
	r := &telemetryDestinationResource{}
	diags := revalidatePlan(context.Background(), planFor(t, r, telemetryDestinationResourceModel{
		DisplayName:    types.StringValue("   "),
		EndpointURL:    types.StringValue("https://otel.example.com/v1/traces"),
		ContentMode:    types.StringValue(client.TelemetryContentModeMetadataOnly),
		ProjectScope:   types.StringValue(client.TelemetryProjectScopeAll),
		ProjectIDs:     types.ListNull(types.StringType),
		RequestHeaders: types.MapNull(types.StringType),
		Enabled:        types.BoolValue(true),
	}))
	if !diags.HasError() {
		t.Error("expected whitespace-only display_name to be rejected")
	}
}
