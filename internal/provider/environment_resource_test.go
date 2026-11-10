package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestEnvironmentSharingDefaultsAndAutoGrantAllowedWithSelected(t *testing.T) {
	ctx := context.Background()
	var sch resource.SchemaResponse
	NewEnvironmentResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	sharingAttr := sch.Schema.Attributes["sharing"].(schema.SingleNestedAttribute)
	for _, field := range []string{"allow_version_pin", "allow_fork", "auto_grant_new_projects"} {
		b := sharingAttr.Attributes[field].(schema.BoolAttribute)
		if b.Default == nil {
			t.Errorf("sharing.%s must have a static false default to avoid Unknown churn on update", field)
		}
	}

	// Environments allow auto_grant_new_projects = true with selected project_ids.
	in, diags := environmentSharingInput(ctx, &workspaceModelSharingModel{
		AllProjects:          types.BoolNull(),
		ProjectIDs:           stringList("proj_1"),
		AllowVersionPin:      types.BoolValue(false),
		AllowFork:            types.BoolValue(false),
		AutoGrantNewProjects: types.BoolValue(true),
	})
	if diags.HasError() {
		t.Fatalf("selected + auto_grant_new_projects should be accepted for environments, got %v", diags)
	}
	if in.AllProjects || !in.AutoGrantNewProjects {
		t.Errorf("unexpected sharing input: %+v", in)
	}
}
