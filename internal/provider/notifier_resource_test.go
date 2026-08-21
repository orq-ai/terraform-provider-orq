package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// fakeNotifiers mirrors the live backend: it DEDUPES the recipient list and
// reports every unset string as "".
type fakeNotifiers struct {
	stored  client.Notifier
	creates int
	updates int
}

func dedupe(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (f *fakeNotifiers) store(in client.NotifierCreateInput) *client.Notifier {
	f.stored = client.Notifier{
		ID:                 "nf_1",
		ProjectID:          in.ProjectID,
		DisplayName:        in.DisplayName,
		Type:               in.Type,
		Emails:             dedupe(in.Emails),
		IncomingWebhookURL: in.IncomingWebhookURL,
		WebhookURL:         in.WebhookURL,
		CreatedAt:          "2026-01-01T00:00:00Z",
		UpdatedAt:          "2026-01-01T00:00:00Z",
	}
	return &f.stored
}

func (f *fakeNotifiers) List(context.Context, client.ListParams) (*client.NotifierPage, error) {
	return &client.NotifierPage{Notifiers: []client.Notifier{f.stored}}, nil
}
func (f *fakeNotifiers) Get(context.Context, string) (*client.Notifier, error) { return &f.stored, nil }
func (f *fakeNotifiers) Create(_ context.Context, in client.NotifierCreateInput) (*client.Notifier, error) {
	f.creates++
	return f.store(in), nil
}
func (f *fakeNotifiers) Update(_ context.Context, in client.NotifierUpdateInput) (*client.Notifier, error) {
	f.updates++
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return f.store(client.NotifierCreateInput{
		ProjectID:          deref(in.ProjectID),
		DisplayName:        deref(in.DisplayName),
		Type:               deref(in.Type),
		Emails:             in.Emails,
		IncomingWebhookURL: deref(in.IncomingWebhookURL),
		WebhookURL:         deref(in.WebhookURL),
	}), nil
}
func (f *fakeNotifiers) Delete(context.Context, string) error { return nil }

func notifierSchema(t *testing.T) schema.Schema {
	t.Helper()
	var sch resource.SchemaResponse
	NewNotifierResource().Schema(context.Background(), resource.SchemaRequest{}, &sch)
	return sch.Schema
}

func notifierRaw(t *testing.T, s schema.Schema, m notifierResourceModel) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := state.Set(ctx, &m); diags.HasError() {
		t.Fatalf("building value: %v", diags)
	}
	return state.Raw
}

func notifierPlan(emails types.List) notifierResourceModel {
	return notifierResourceModel{
		ID:                 types.StringNull(),
		DisplayName:        types.StringValue("alerts"),
		Type:               types.StringValue(client.NotifierTypeEmail),
		ProjectID:          types.StringNull(),
		Emails:             emails,
		IncomingWebhookURL: types.StringNull(),
		WebhookURL:         types.StringNull(),
		CreatedAt:          types.StringNull(),
		UpdatedAt:          types.StringNull(),
	}
}

// TestNotifierPreservesEmptyEmails proves an explicitly empty recipient list
// survives the round trip. Mapping a zero-length read-back to null made
// `emails = []` an inconsistent result after apply: the notifier was created,
// the apply failed, and every retry orphaned another one — the config could
// never converge.
func TestNotifierPreservesEmptyEmails(t *testing.T) {
	ctx := context.Background()
	s := notifierSchema(t)
	emptyState := func() tfsdk.State {
		return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	}

	for _, tc := range []struct {
		name     string
		emails   types.List
		wantNull bool
	}{
		{"explicitly empty stays empty", stringList(), false},
		{"omitted stays null", types.ListNull(types.StringType), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeNotifiers{}
			r := &notifierResource{notifiers: api}

			createResp := resource.CreateResponse{State: emptyState()}
			r.Create(ctx, resource.CreateRequest{
				Plan: tfsdk.Plan{Schema: s, Raw: notifierRaw(t, s, notifierPlan(tc.emails))},
			}, &createResp)
			if createResp.Diagnostics.HasError() {
				t.Fatalf("Create: %v", createResp.Diagnostics)
			}
			var created notifierResourceModel
			if diags := createResp.State.Get(ctx, &created); diags.HasError() {
				t.Fatalf("reading state: %v", diags)
			}
			if created.Emails.IsNull() != tc.wantNull {
				t.Fatalf("emails null = %v, want %v", created.Emails.IsNull(), tc.wantNull)
			}

			// The refresh must be a fixed point, or the diff never settles.
			readResp := resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: notifierRaw(t, s, created)}}
			r.Read(ctx, resource.ReadRequest{State: readResp.State}, &readResp)
			if readResp.Diagnostics.HasError() {
				t.Fatalf("Read: %v", readResp.Diagnostics)
			}
			var refreshed notifierResourceModel
			if diags := readResp.State.Get(ctx, &refreshed); diags.HasError() {
				t.Fatalf("reading state: %v", diags)
			}
			if !refreshed.Emails.Equal(created.Emails) {
				t.Errorf("a refresh must not change emails: %v -> %v", created.Emails, refreshed.Emails)
			}

			// And so must an update: it maps the write response through the same
			// projection, so the shape has to survive that path too.
			updatePlan := notifierPlan(tc.emails)
			updatePlan.ID = created.ID
			updateResp := resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: notifierRaw(t, s, created)}}
			r.Update(ctx, resource.UpdateRequest{
				Plan:  tfsdk.Plan{Schema: s, Raw: notifierRaw(t, s, updatePlan)},
				State: tfsdk.State{Schema: s, Raw: notifierRaw(t, s, created)},
			}, &updateResp)
			if updateResp.Diagnostics.HasError() {
				t.Fatalf("Update: %v", updateResp.Diagnostics)
			}
			var updated notifierResourceModel
			if diags := updateResp.State.Get(ctx, &updated); diags.HasError() {
				t.Fatalf("reading state: %v", diags)
			}
			if !updated.Emails.Equal(created.Emails) {
				t.Errorf("an update must not change emails: %v -> %v", created.Emails, updated.Emails)
			}
		})
	}
}

// The same empty-vs-absent hazard applies to the webhook URLs, which take any
// string. project_id is not covered because it cannot be "": it carries a
// non-empty validator, at plan time and again on the resolved plan.
func TestNotifierPreservesEmptyStrings(t *testing.T) {
	r := &notifierResource{}
	m := notifierResourceModel{
		ProjectID:          types.StringNull(),
		WebhookURL:         types.StringValue(""),
		IncomingWebhookURL: types.StringNull(),
		Emails:             types.ListNull(types.StringType),
	}
	r.apply(&client.Notifier{ID: "nf_1", Type: client.NotifierTypeWebhook}, &m)
	if m.WebhookURL.IsNull() {
		t.Error("a configured empty string must not read back as null")
	}
	if !m.IncomingWebhookURL.IsNull() {
		t.Error("an unset string must stay null")
	}
}

// An explicitly empty webhook URL has to survive the update path as well, which
// maps the write response rather than a read.
func TestNotifierUpdateKeepsEmptyWebhookURL(t *testing.T) {
	ctx := context.Background()
	s := notifierSchema(t)
	model := notifierResourceModel{
		ID:                 types.StringValue("nf_1"),
		DisplayName:        types.StringValue("hook"),
		Type:               types.StringValue(client.NotifierTypeWebhook),
		ProjectID:          types.StringNull(),
		Emails:             types.ListNull(types.StringType),
		IncomingWebhookURL: types.StringNull(),
		WebhookURL:         types.StringValue(""),
		CreatedAt:          types.StringNull(),
		UpdatedAt:          types.StringNull(),
	}
	raw := notifierRaw(t, s, model)

	api := &fakeNotifiers{}
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	(&notifierResource{notifiers: api}).Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: raw},
		State: tfsdk.State{Schema: s, Raw: raw},
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Update: %v", resp.Diagnostics)
	}
	var updated notifierResourceModel
	if diags := resp.State.Get(ctx, &updated); diags.HasError() {
		t.Fatalf("reading state: %v", diags)
	}
	if updated.WebhookURL.IsNull() {
		t.Error("a configured empty webhook_url must survive an update")
	}
	if !updated.IncomingWebhookURL.IsNull() {
		t.Error("an unset url must stay null")
	}
}

// TestNotifierDuplicateEmailsRejectedBeforeTheWrite covers the other half of the
// same failure: the server deduplicates, so a repeated address makes an element
// "vanish" from the read-back — again tainting an already-created notifier.
func TestNotifierDuplicateEmailsRejectedBeforeTheWrite(t *testing.T) {
	ctx := context.Background()
	s := notifierSchema(t)
	emptyState := func() tfsdk.State {
		return tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	}

	for _, tc := range []struct {
		name   string
		emails types.List
		want   string
	}{
		{"duplicate address", stringList("a@x.io", "a@x.io"), "must not repeat an address"},
		{"null element", types.ListValueMust(types.StringType, []attr.Value{types.StringNull()}), "must not contain a null element"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := tfsdk.Plan{Schema: s, Raw: notifierRaw(t, s, notifierPlan(tc.emails))}
			check := func(diags diag.Diagnostics, api *fakeNotifiers) {
				t.Helper()
				if !diags.HasError() {
					t.Fatal("the recipient list must be rejected")
				}
				var details []string
				for _, e := range diags.Errors() {
					details = append(details, e.Detail())
				}
				if !strings.Contains(strings.Join(details, "\n"), tc.want) {
					t.Errorf("the diagnostic must explain the constraint, got %q", details)
				}
				if api.creates != 0 || api.updates != 0 {
					t.Errorf("no notifier must be written: %d creates, %d updates", api.creates, api.updates)
				}
			}

			api := &fakeNotifiers{}
			createResp := resource.CreateResponse{State: emptyState()}
			(&notifierResource{notifiers: api}).Create(ctx, resource.CreateRequest{Plan: plan}, &createResp)
			check(createResp.Diagnostics, api)

			api = &fakeNotifiers{}
			updateResp := resource.UpdateResponse{State: emptyState()}
			(&notifierResource{notifiers: api}).Update(ctx, resource.UpdateRequest{Plan: plan, State: emptyState()}, &updateResp)
			check(updateResp.Diagnostics, api)
		})
	}
}

// A literal duplicate is caught by the schema validators, before apply.
func TestNotifierEmailsRejectedAtPlanTime(t *testing.T) {
	ctx := context.Background()
	validators := notifierSchema(t).Attributes["emails"].(schema.ListAttribute).Validators

	for _, tc := range []struct {
		name      string
		value     types.List
		wantError bool
	}{
		{"unique accepted", stringList("a@x.io", "b@x.io"), false},
		{"empty accepted", stringList(), false},
		{"duplicate rejected", stringList("a@x.io", "a@x.io"), true},
		{"null element rejected", types.ListValueMust(types.StringType, []attr.Value{types.StringNull()}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diags diag.Diagnostics
			for _, v := range validators {
				resp := &validator.ListResponse{}
				v.ValidateList(ctx, validator.ListRequest{
					Path:           path.Root("emails"),
					PathExpression: path.MatchRoot("emails"),
					ConfigValue:    tc.value,
				}, resp)
				diags.Append(resp.Diagnostics...)
			}
			if diags.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v (%v)", diags.HasError(), tc.wantError, diags)
			}
		})
	}
}
