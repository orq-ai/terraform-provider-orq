package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

func TestSecretNameValidatorsRejectReservedNames(t *testing.T) {
	ctx := context.Background()
	var sch resource.SchemaResponse
	NewSecretResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	nameAttr := sch.Schema.Attributes["name"].(schema.StringAttribute)

	cases := []struct {
		name    string
		wantErr bool
	}{
		{"STRIPE_API_KEY", false},
		{"A1_B2", false},
		{"_PRIVATE", true},
		{"ORQ_TOKEN", true},
		{"1BAD", true},
		{"lowercase", true},
	}
	for _, tc := range cases {
		var hasErr bool
		for _, v := range nameAttr.Validators {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{
				Path:        path.Root("name"),
				ConfigValue: types.StringValue(tc.name),
			}, &resp)
			if resp.Diagnostics.HasError() {
				hasErr = true
			}
		}
		if hasErr != tc.wantErr {
			t.Errorf("name=%q: hasErr=%v, want %v", tc.name, hasErr, tc.wantErr)
		}
	}
}

func TestRotatedSecretValueOnlySendsOnChange(t *testing.T) {
	if got := rotatedSecretValue(types.StringValue("same"), types.StringValue("same")); got != nil {
		t.Errorf("unchanged secret value must yield nil, got %q", *got)
	}
	if got := rotatedSecretValue(types.StringValue("new"), types.StringValue("old")); got == nil || *got != "new" {
		t.Errorf("changed secret value must yield \"new\", got %v", got)
	}
}

func TestSecretResourceApplyPreservesWriteOnlyValue(t *testing.T) {
	r := &secretResource{}
	m := secretResourceModel{Value: types.StringValue("super-secret")}
	r.apply(&client.Secret{
		ID:      "sec_1",
		VaultID: "vault_1",
		Name:    "API_KEY",
		Kind:    client.SecretKindSecret,
	}, &m)
	if m.Value.ValueString() != "super-secret" {
		t.Errorf("apply must preserve write-only value, got %v", m.Value)
	}
}
