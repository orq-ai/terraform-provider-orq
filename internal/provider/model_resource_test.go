package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// TestModelApplyPreservesSecretAndMetadata is the #1 correctness proof: apply()
// (used by Create/Read/Update) never clobbers the config-authoritative fields —
// the secret api_key (never echoed by the server) and the derived metadata
// optionals — while it DOES refresh the reliably round-tripped identity fields.
func TestModelApplyPreservesSecretAndMetadata(t *testing.T) {
	r := &modelResource{}
	// `m` stands in for the plan (Create/Update) or prior state (Read): it carries
	// the operator's configured secret + metadata.
	m := &modelResourceModel{
		APIKey:              types.StringValue("sk-secret-xyz"),
		InputCost:           types.Float64Value(0.5),
		OutputCost:          types.Float64Value(1.5),
		MaxTokens:           types.Int64Value(8192),
		Temperature:         types.Float64Value(0.7),
		HasReasoning:        types.BoolValue(true),
		SupportsVision:      types.BoolValue(true),
		SupportsToolCalling: types.BoolValue(true),
		SupportsStrictTool:  types.BoolValue(false),
		SupportsImageEdit:   types.BoolValue(false),
		CostPerImage:        types.Float64Value(0.02),
		// stale identity values that MUST be refreshed from the server projection:
		Region:  types.StringValue("europe"),
		BaseURL: types.StringValue("http://host.docker.internal:1234/v1"),
	}

	// Server projection: has the identity fields but NO api_key and NO metadata
	// (exactly what the real API returns).
	r.apply(&client.Model{
		ID:          "mdl_uuid_1",
		DisplayName: "tf model",
		ModelID:     "liquid/lfm2.5-1.2b",
		ModelType:   "chat",
		Region:      "europe",
		BaseURL:     "http://host.docker.internal:1234/v1",
		Description: "a custom model",
		Created:     "2020-01-01T00:00:00Z",
		Updated:     "2020-01-02T00:00:00Z",
	}, m)

	// Secret preserved (server never returns it).
	if m.APIKey.ValueString() != "sk-secret-xyz" {
		t.Errorf("apply() must preserve api_key, got %q", m.APIKey.ValueString())
	}
	// Metadata optionals preserved (not refreshed → no false drift).
	if m.InputCost.ValueFloat64() != 0.5 || m.OutputCost.ValueFloat64() != 1.5 {
		t.Errorf("apply() clobbered cost metadata: in=%v out=%v", m.InputCost, m.OutputCost)
	}
	if m.MaxTokens.ValueInt64() != 8192 || m.Temperature.ValueFloat64() != 0.7 {
		t.Errorf("apply() clobbered max_tokens/temperature: %v %v", m.MaxTokens, m.Temperature)
	}
	if !m.HasReasoning.ValueBool() || !m.SupportsVision.ValueBool() || !m.SupportsToolCalling.ValueBool() {
		t.Errorf("apply() clobbered capability bools")
	}
	if m.SupportsStrictTool.ValueBool() || m.SupportsImageEdit.ValueBool() {
		t.Errorf("apply() flipped a false capability bool")
	}
	if m.CostPerImage.ValueFloat64() != 0.02 {
		t.Errorf("apply() clobbered cost_per_image: %v", m.CostPerImage)
	}

	// Identity fields refreshed from the server.
	if m.ID.ValueString() != "mdl_uuid_1" || m.DisplayName.ValueString() != "tf model" {
		t.Errorf("identity not refreshed: id=%q name=%q", m.ID.ValueString(), m.DisplayName.ValueString())
	}
	if m.ModelID.ValueString() != "liquid/lfm2.5-1.2b" || m.ModelType.ValueString() != "chat" {
		t.Errorf("model_id/model_type not refreshed: %q %q", m.ModelID.ValueString(), m.ModelType.ValueString())
	}
	if m.Region.ValueString() != "europe" || m.BaseURL.ValueString() != "http://host.docker.internal:1234/v1" {
		t.Errorf("region/base_url not refreshed: %q %q", m.Region.ValueString(), m.BaseURL.ValueString())
	}
	if m.Description.ValueString() != "a custom model" {
		t.Errorf("description should round-trip, got %q", m.Description.ValueString())
	}
	if m.Created.ValueString() != "2020-01-01T00:00:00Z" || m.Updated.ValueString() != "2020-01-02T00:00:00Z" {
		t.Errorf("timestamps not refreshed: %q %q", m.Created.ValueString(), m.Updated.ValueString())
	}
}

// TestModelApplyRegionBaseURLEmptyFallback proves a Required attribute never
// reads back empty when the server omits the (optional-on-the-wire)
// configuration.region / base_url: apply keeps the configured value.
func TestModelApplyRegionBaseURLEmptyFallback(t *testing.T) {
	r := &modelResource{}
	m := &modelResourceModel{
		Region:  types.StringValue("europe"),
		BaseURL: types.StringValue("http://host/v1"),
	}
	r.apply(&client.Model{ID: "mdl_1", Region: "", BaseURL: ""}, m)
	if m.Region.ValueString() != "europe" {
		t.Errorf("empty server region must fall back to config, got %q", m.Region.ValueString())
	}
	if m.BaseURL.ValueString() != "http://host/v1" {
		t.Errorf("empty server base_url must fall back to config, got %q", m.BaseURL.ValueString())
	}
}

// TestModelSchemaDecisions locks in the update-vs-replace + secret decisions:
// api_key is Sensitive, Required and RequiresReplace; model_type constrains its
// enum; the identity fields (model_id/model_type/region/base_url) carry no
// RequiresReplace (they are mutable in place).
func TestModelSchemaDecisions(t *testing.T) {
	ctx := context.Background()
	var sch resource.SchemaResponse
	NewModelResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	if sch.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", sch.Diagnostics)
	}

	apiKey, ok := sch.Schema.Attributes["api_key"].(schema.StringAttribute)
	if !ok {
		t.Fatal("api_key is not a StringAttribute")
	}
	if !apiKey.Sensitive {
		t.Error("api_key must be Sensitive")
	}
	if !apiKey.Required {
		t.Error("api_key must be Required")
	}
	if len(apiKey.PlanModifiers) == 0 {
		t.Error("api_key must carry a RequiresReplace plan modifier (secret is immutable in place)")
	} else {
		desc := strings.ToLower(apiKey.PlanModifiers[0].Description(ctx))
		if !strings.Contains(desc, "destroy") || !strings.Contains(desc, "recreate") {
			t.Errorf("api_key plan modifier is not RequiresReplace: %q", desc)
		}
	}

	modelType, ok := sch.Schema.Attributes["model_type"].(schema.StringAttribute)
	if !ok {
		t.Fatal("model_type is not a StringAttribute")
	}
	if !modelType.Required || len(modelType.Validators) == 0 {
		t.Error("model_type must be Required with an enum validator")
	}

	// Fields the update endpoint accepts are mutable in place: no RequiresReplace.
	for _, name := range []string{"model_id", "model_type", "region", "base_url", "display_name"} {
		attr, ok := sch.Schema.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a StringAttribute", name)
		}
		for _, pm := range attr.PlanModifiers {
			d := strings.ToLower(pm.Description(ctx))
			if strings.Contains(d, "destroy") && strings.Contains(d, "recreate") {
				t.Errorf("%s must be mutable in place (no RequiresReplace)", name)
			}
		}
	}
}
