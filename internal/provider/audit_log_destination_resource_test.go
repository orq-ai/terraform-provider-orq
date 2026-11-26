package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

type fakeAuditLogDestinationsAPI struct {
	updates        []client.AuditLogDestinationUpdateInput
	failResumeCall bool
}

func (f *fakeAuditLogDestinationsAPI) Get(context.Context, string) (*client.AuditLogDestination, error) {
	return nil, nil
}
func (f *fakeAuditLogDestinationsAPI) Create(context.Context, client.AuditLogDestinationCreateInput) (*client.AuditLogDestination, string, error) {
	return nil, "", nil
}
func (f *fakeAuditLogDestinationsAPI) Update(_ context.Context, in client.AuditLogDestinationUpdateInput) (*client.AuditLogDestination, error) {
	f.updates = append(f.updates, in)
	if f.failResumeCall && len(f.updates) == 2 {
		return nil, &client.Error{Code: client.CodeInternal, Message: "resume failed"}
	}
	url := "https://new.example.com/hook"
	if in.URL != nil {
		url = *in.URL
	}
	return &client.AuditLogDestination{
		ID:                in.ID,
		DisplayName:       "Audit",
		URL:               url,
		Status:            *in.Status,
		SigningSecretHint: "abcd",
		SecretRotatedAt:   "2026-11-10T00:00:00Z",
		CreatedAt:         "2026-11-10T00:00:00Z",
		UpdatedAt:         "2026-11-10T00:00:01Z",
	}, nil
}
func (f *fakeAuditLogDestinationsAPI) Delete(context.Context, string) error { return nil }

func TestAuditLogDestinationUpdatePersistsPausedStateOnFailedResume(t *testing.T) {
	ctx := context.Background()
	api := &fakeAuditLogDestinationsAPI{failResumeCall: true}
	r := &auditLogDestinationResource{destinations: api}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)

	priorModel := auditLogDestinationResourceModel{
		ID:                types.StringValue("dest_1"),
		DisplayName:       types.StringValue("Audit"),
		Description:       types.StringNull(),
		URL:               types.StringValue("https://old.example.com/hook"),
		Status:            types.StringValue(client.AuditLogDestinationStatusActive),
		SigningSecret:     types.StringValue("whsec_secret"),
		SigningSecretHint: types.StringValue("abcd"),
		SecretRotatedAt:   types.StringValue("2026-11-10T00:00:00Z"),
		CreatedAt:         types.StringValue("2026-11-10T00:00:00Z"),
		UpdatedAt:         types.StringValue("2026-11-10T00:00:00Z"),
	}
	planModel := priorModel
	planModel.URL = types.StringValue("https://new.example.com/hook")

	state := tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}
	state.Set(ctx, &priorModel)
	resp := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: planFor(t, r, planModel), State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error when resume fails")
	}

	var got auditLogDestinationResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("State.Get: %v", diags)
	}
	if got.Status.ValueString() != client.AuditLogDestinationStatusPaused {
		t.Errorf("state status = %q, want PAUSED after failed resume", got.Status.ValueString())
	}
	if got.URL.ValueString() != "https://new.example.com/hook" {
		t.Errorf("state url = %q, want new url", got.URL.ValueString())
	}
	if got.SigningSecret.ValueString() != "whsec_secret" {
		t.Errorf("signing_secret must be preserved, got %q", got.SigningSecret.ValueString())
	}
}

func TestAuditLogDestinationRejectsSurroundingWhitespace(t *testing.T) {
	r := &auditLogDestinationResource{}
	diags := revalidatePlan(context.Background(), planFor(t, r, auditLogDestinationResourceModel{
		DisplayName: types.StringValue(" padded "),
		URL:         types.StringValue("https://example.com/hook"),
		Status:      types.StringValue(client.AuditLogDestinationStatusActive),
	}))
	if !diags.HasError() {
		t.Error("expected padded display_name to be rejected")
	}
}
