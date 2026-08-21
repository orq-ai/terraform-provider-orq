package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// TestProjectKeyPlanValue proves both update paths of the key plan modifier:
// an unchanged name keeps the prior key (no spurious diff), while a rename marks
// key unknown ("known after apply") because the server re-derives it — the fix
// for "inconsistent result after apply: .key".
func TestProjectKeyPlanValue(t *testing.T) {
	prior := types.StringValue("production-a1b2")

	// Name unchanged → keep the prior key.
	got := projectKeyPlanValue(types.StringValue("Production"), types.StringValue("Production"), prior)
	if got.IsUnknown() || got.ValueString() != "production-a1b2" {
		t.Errorf("unchanged name must keep the prior key, got %+v", got)
	}

	// Name changed → key is unknown.
	got = projectKeyPlanValue(types.StringValue("Staging"), types.StringValue("Production"), prior)
	if !got.IsUnknown() {
		t.Errorf("renamed project must mark key unknown, got %+v", got)
	}
}

// TestProjectKeySchemaUsesCustomModifier guards against a regression back to
// UseStateForUnknown (which keeps the stale key across a rename).
func TestProjectKeySchemaUsesCustomModifier(t *testing.T) {
	var sch resource.SchemaResponse
	NewProjectResource().Schema(context.Background(), resource.SchemaRequest{}, &sch)
	key, ok := sch.Schema.Attributes["key"].(schema.StringAttribute)
	if !ok {
		t.Fatal("key is not a StringAttribute")
	}
	if len(key.PlanModifiers) != 1 {
		t.Fatalf("key must carry exactly one plan modifier, got %d", len(key.PlanModifiers))
	}
	if _, ok := key.PlanModifiers[0].(projectKeyPlanModifier); !ok {
		t.Errorf("key must use projectKeyPlanModifier, got %T", key.PlanModifiers[0])
	}
}

// A description an operator set to "" must survive the refresh: the server
// echoes "" for both a stored empty string and an unset field, so collapsing it
// to null planned `description: null -> ""` on every apply, forever.
func TestProjectApplyPreservesEmptyDescription(t *testing.T) {
	r := &projectResource{}
	stored := &client.Project{ID: "p1", Name: "acme", Key: "acme"}

	kept := projectResourceModel{Description: types.StringValue("")}
	r.apply(stored, &kept)
	if kept.Description.IsNull() {
		t.Error("a configured empty description must survive the refresh")
	}

	omitted := projectResourceModel{Description: types.StringNull()}
	r.apply(stored, &omitted)
	if !omitted.Description.IsNull() {
		t.Error("an omitted description must stay null")
	}
}
