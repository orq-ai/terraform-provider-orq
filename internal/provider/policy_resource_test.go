package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestPolicySlugPlanValue proves both update paths of the slug plan modifier:
// an unchanged display_name keeps the prior slug, while a rename marks slug
// unknown because the server re-derives it — the fix for "inconsistent result
// after apply: .slug" (mirrors projectKeyPlanValue).
func TestPolicySlugPlanValue(t *testing.T) {
	prior := types.StringValue("my-policy")
	got := policySlugPlanValue(types.StringValue("My Policy"), types.StringValue("My Policy"), prior)
	if got.IsUnknown() || got.ValueString() != "my-policy" {
		t.Errorf("unchanged display_name must keep the prior slug, got %+v", got)
	}
	got = policySlugPlanValue(types.StringValue("Renamed"), types.StringValue("My Policy"), prior)
	if !got.IsUnknown() {
		t.Errorf("renamed policy must mark slug unknown, got %+v", got)
	}
}
