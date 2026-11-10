package provider

import (
	"context"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

type fakeVaultsAPI struct {
	created client.VaultCreateInput
	updated client.VaultUpdateInput
	vault   *client.Vault
}

func (f *fakeVaultsAPI) Get(context.Context, string) (*client.Vault, error) { return f.vault, nil }
func (f *fakeVaultsAPI) Create(_ context.Context, in client.VaultCreateInput) (*client.Vault, error) {
	f.created = in
	v := &client.Vault{
		ID:          "vault_1",
		DisplayName: in.DisplayName,
		Description: in.Description,
		FactoryID:   in.FactoryID,
		Sharing:     in.Sharing,
		CreatedAt:   "2026-11-10T00:00:00Z",
		UpdatedAt:   "2026-11-10T00:00:00Z",
	}
	f.vault = v
	return v, nil
}
func (f *fakeVaultsAPI) Update(_ context.Context, in client.VaultUpdateInput) (*client.Vault, error) {
	f.updated = in
	v := &client.Vault{
		ID:          in.ID,
		DisplayName: *in.DisplayName,
		Description: *in.Description,
		Sharing:     in.Sharing,
		CreatedAt:   "2026-11-10T00:00:00Z",
		UpdatedAt:   "2026-11-10T00:00:01Z",
	}
	f.vault = v
	return v, nil
}
func (f *fakeVaultsAPI) Delete(context.Context, string) error { return nil }

func TestVaultResourceSharingProjectIDsAndEmptyList(t *testing.T) {
	ctx := context.Background()
	api := &fakeVaultsAPI{}
	r := &vaultResource{vaults: api}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)

	// Selected with both factory_ids and project_ids.
	plan := planAfterMarking(t, r, vaultResourceModel{
		DisplayName:      types.StringValue("Shared Vault"),
		FactoryID:        types.StringNull(),
		SharedFactoryIDs: stringList("fac_1"),
		SharedProjectIDs: stringList("proj_2", "proj_1"),
	})
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create: %v", resp.Diagnostics)
	}
	if api.created.Sharing == nil || api.created.Sharing.AllFactories {
		t.Fatalf("expected selected sharing, got %+v", api.created.Sharing)
	}
	if !slices.Equal(api.created.Sharing.ProjectIDs, []string{"proj_2", "proj_1"}) {
		t.Errorf("project_ids = %v, want [proj_2 proj_1]", api.created.Sharing.ProjectIDs)
	}

	// Empty shared_factory_ids stays [] (non-null) while omitted shared_project_ids stays null.
	planEmpty := planAfterMarking(t, r, vaultResourceModel{
		DisplayName:      types.StringValue("Empty Selected"),
		FactoryID:        types.StringNull(),
		SharedFactoryIDs: stringList(),
		SharedProjectIDs: types.ListNull(types.StringType),
	})
	respEmpty := resource.CreateResponse{State: tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, resource.CreateRequest{Plan: planEmpty}, &respEmpty)
	if respEmpty.Diagnostics.HasError() {
		t.Fatalf("Create empty: %v", respEmpty.Diagnostics)
	}
	var got vaultResourceModel
	if diags := respEmpty.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("State.Get: %v", diags)
	}
	if got.SharedFactoryIDs.IsNull() || len(got.SharedFactoryIDs.Elements()) != 0 {
		t.Errorf("shared_factory_ids must stay non-null empty list, got %v", got.SharedFactoryIDs)
	}
	if !got.SharedProjectIDs.IsNull() {
		t.Errorf("omitted shared_project_ids must stay null, got %v", got.SharedProjectIDs)
	}
}
