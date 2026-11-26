package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestSmartRouterRejectsNullAndUnicodeWhitespaceModels(t *testing.T) {
	r := &smartRouterResource{}

	diags := revalidatePlan(context.Background(), planFor(t, r, smartRouterResourceModel{
		Key:     types.StringValue("router-1"),
		Profile: types.StringValue("balanced"),
		Models: types.ListValueMust(types.StringType, []attr.Value{
			types.StringValue("openai/gpt-4o"),
			types.StringNull(),
		}),
		Enabled: types.BoolValue(true),
	}))
	if !diags.HasError() {
		t.Error("expected null element in models to be rejected")
	}

	diags = revalidatePlan(context.Background(), planFor(t, r, smartRouterResourceModel{
		Key:     types.StringValue("router-1"),
		Profile: types.StringValue("balanced"),
		Models:  stringList("openai/gpt-4o", "\u00a0openai/gpt-4o-mini"),
		Enabled: types.BoolValue(true),
	}))
	if !diags.HasError() {
		t.Error("expected non-ASCII surrounding whitespace in model ref to be rejected")
	}
}
