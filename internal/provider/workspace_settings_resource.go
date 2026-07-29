package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                = &workspaceSettingsResource{}
	_ resource.ResourceWithConfigure   = &workspaceSettingsResource{}
	_ resource.ResourceWithImportState = &workspaceSettingsResource{}
)

// piiLanguages / piiFailureModes are the closed enums the server validates
// (libs/go/models plugins.go). They are small and stable, so mirroring them
// client-side turns an apply-time 400 into a `terraform validate` error.
//
// The ENTITY catalog is deliberately NOT mirrored: it is per-language, long, and
// grows with every detector release — a client-side copy would reject values the
// server accepts. Entities are validated server-side only.
var (
	piiLanguages    = []string{"en", "nl"}
	piiFailureModes = []string{"block", "passthrough"}
)

// displayNameWhitespaceValidator rejects a display_name that is not already
// trimmed (or that is whitespace-only). See the schema description for why this
// is a plan-time error rather than a silent normalization.
//
// It deliberately uses strings.TrimSpace — byte-for-byte the same call the
// server makes in normalizeDisplayName (apps/platform-api/workspacesettings
// connect_routes.go) — rather than a regexp. Go's `\S` is ASCII-only, so a
// pattern like `^\S(.*\S)?$` accepts a name padded with U+00A0 (NBSP) or U+2003
// (EM SPACE) that the server's Unicode-aware TrimSpace strips, which is exactly
// the perpetual diff this validator exists to prevent.
type displayNameWhitespaceValidator struct{}

func (displayNameWhitespaceValidator) Description(context.Context) string {
	return "must not begin or end with whitespace and must not be whitespace-only (the server trims it, which would cause a perpetual diff)"
}

func (v displayNameWhitespaceValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (displayNameWhitespaceValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Empty workspace display name",
			"display_name must contain at least one non-whitespace character: the server rejects a name that is "+
				"empty or only whitespace.")
		return
	}
	if trimmed != value {
		resp.Diagnostics.AddAttributeError(req.Path, "Leading or trailing whitespace in display name",
			fmt.Sprintf("display_name must not begin or end with whitespace (this includes non-ASCII whitespace such "+
				"as U+00A0). The server trims it and would store %q, so the configured value would never match state — "+
				"either a perpetual diff or an \"inconsistent result after apply\" error. Write it as %q.",
				trimmed, trimmed))
	}
}

// NewWorkspaceSettingsResource is the factory registered on the provider.
func NewWorkspaceSettingsResource() resource.Resource { return &workspaceSettingsResource{} }

type workspaceSettingsResource struct {
	settings client.WorkspaceSettingsAPI
}

type workspaceSettingsResourceModel struct {
	Key                  types.String               `tfsdk:"key"`
	DisplayName          types.String               `tfsdk:"display_name"`
	EnforceEnabledModels types.Bool                 `tfsdk:"enforce_enabled_models"`
	PiiRedaction         *workspaceSettingsPiiModel `tfsdk:"pii_redaction"`
}

type workspaceSettingsPiiModel struct {
	Enabled types.Bool                       `tfsdk:"enabled"`
	Config  *workspaceSettingsPiiConfigModel `tfsdk:"config"`
}

type workspaceSettingsPiiConfigModel struct {
	Language  types.String  `tfsdk:"language"`
	Entities  types.List    `tfsdk:"entities"`
	OnFailure types.String  `tfsdk:"on_failure"`
	Threshold types.Float64 `tfsdk:"threshold"`
}

func (r *workspaceSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace_settings"
}

func (r *workspaceSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Workspace-level settings. The workspace IS the tenant of the management key, so " +
			"this is a SINGLETON with no id: there is exactly one per workspace and the API offers only read " +
			"and partial update.\n\n" +
			"**Lifecycle:** `create` ADOPTS the existing settings — it writes the attributes this resource sets " +
			"and reads the rest back; it never creates anything. `destroy` only removes the resource from " +
			"Terraform state: NO server call is made and every setting keeps its last applied value. `import` " +
			"accepts any id (use the sentinel `workspace`) and simply reads the settings.\n\n" +
			"**Partial management:** every attribute is optional. An attribute you do not set is left alone " +
			"server-side — in particular, omitting `pii_redaction` means \"this resource does not manage PII " +
			"redaction\" and is NEVER equivalent to `enabled = false`; the block is not read into state at all, " +
			"so an out-of-band change to it produces no diff. Setting `pii_redaction` makes it managed, and it " +
			"is then a FULL REPLACE: a field you drop from the block is DROPPED server-side on the next apply.\n\n" +
			"Declaring this resource more than once for the same workspace makes two configurations fight over " +
			"one object — declare it once.",
		Attributes: map[string]schema.Attribute{
			"key": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Read-only workspace key/slug. It is embedded in resource URLs and API " +
					"credentials, so the API deliberately offers no way to change it.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"display_name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Human-readable workspace name (1-128 characters). Optional+Computed: " +
					"omit it and the current server value is adopted into state and left unchanged. It cannot be " +
					"cleared — the server rejects an empty or whitespace-only name.\n\n" +
					"Leading/trailing whitespace is REJECTED at plan time rather than silently trimmed: the " +
					"server trims the value it stores, so a config of `\" x \"` would read back as `\"x\"` and " +
					"produce either a perpetual diff or an \"inconsistent result after apply\" error. A " +
					"plan-time error names the problem instead of hiding it. The check mirrors the server's " +
					"Unicode-aware trim, so padding with a non-breaking space (U+00A0) is rejected too.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 128),
					displayNameWhitespaceValidator{},
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"enforce_enabled_models": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether only workspace-enabled models may be served. Optional+Computed: " +
					"omit it and the current server value is adopted into state and left unchanged.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"pii_redaction": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Workspace-default PII redaction plugin configuration, applied as a floor " +
					"to gateway requests that send no plugins of their own.\n\n" +
					"OMITTING this block means the resource does not manage PII redaction: nothing is sent on " +
					"apply and the server value is not read into state, so there is no diff either way. That is " +
					"NOT the same as `enabled = false`, which actively turns the workspace default off.\n\n" +
					"When the block IS present it fully REPLACES the stored object on every apply: any nested " +
					"field you remove from config is removed server-side too (it does not retain its last value).",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Required: true,
						MarkdownDescription: "Whether the workspace-default PII redaction plugin is enabled. " +
							"Required so that a managed block always states its intent explicitly — an implied " +
							"default here would be indistinguishable from \"unmanaged\". Consequently " +
							"`pii_redaction = {}` is INTENTIONALLY a configuration error: an empty block would be " +
							"ambiguous between \"manage it, disabled\" and \"do not manage it\", so it must be " +
							"spelled either `pii_redaction = { enabled = false }` or omitted entirely.",
					},
					"config": schema.SingleNestedAttribute{
						Optional: true,
						MarkdownDescription: "Plugin configuration applied when enabled. Omit the whole block to " +
							"store no config at all (the gateway then applies its own defaults). Part of the " +
							"full-replace payload — see `pii_redaction`.",
						Attributes: map[string]schema.Attribute{
							"language": schema.StringAttribute{
								Optional: true,
								MarkdownDescription: "Detector language: `en` or `nl`. Omit for the gateway " +
									"default (`en`). The valid `entities` catalog depends on this value.",
								Validators: []validator.String{stringvalidator.OneOf(piiLanguages...)},
							},
							"entities": schema.ListAttribute{
								Optional:    true,
								ElementType: types.StringType,
								MarkdownDescription: "Entity types to redact (e.g. `EMAIL_ADDRESS`, `PERSON`). " +
									"An EMPTY list `[]` means \"redact every type the detector finds\"; OMITTING " +
									"the attribute leaves it out of the stored config entirely (which the gateway " +
									"treats the same way, but keeps the two spellings distinct in state).\n\n" +
									"Values are validated against the per-language catalog SERVER-side only: the " +
									"catalog is long, language-dependent and grows with the detector, so mirroring " +
									"it in the provider would reject values the server accepts. An unknown entity " +
									"is rejected at apply, not at plan.\n\n" +
									"Entities round-trip VERBATIM, so no canonical spelling is imposed on your " +
									"config: the server upper-cases and trims a COPY of each value purely to look " +
									"it up in the catalog, but stores — and returns from both read and update — " +
									"exactly the strings and the order that were sent. (The gateway canonicalises " +
									"only the effective per-request plugin it assembles at call time, which is " +
									"not this stored value.)",
								Validators: []validator.List{
									listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
								},
							},
							"on_failure": schema.StringAttribute{
								Optional: true,
								MarkdownDescription: "Behaviour when redaction cannot run: `block` (fail closed, " +
									"the gateway default) or `passthrough` (fail open, send the original text).",
								Validators: []validator.String{stringvalidator.OneOf(piiFailureModes...)},
							},
							"threshold": schema.Float64Attribute{
								Optional:            true,
								MarkdownDescription: "Detection confidence threshold, in the `[0, 1]` range.",
								Validators:          []validator.Float64{float64validator.Between(0, 1)},
							},
						},
					},
				},
			},
		},
	}
}

func (r *workspaceSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.settings = c.WorkspaceSettings()
}

// updateInput builds the partial-update payload. A null/unknown attribute yields
// a nil pointer, which the client omits from the request — so an attribute this
// resource does not set is never written and keeps its server value.
// pii_redaction is all-or-nothing: absent means "not managed", present means
// "replace the stored object with exactly this".
//
// Callers pass the CONFIG, never the plan. For an Optional+Computed attribute a
// null config plans as the PRIOR STATE, so a plan-derived payload would re-send
// values the operator never asked this resource to manage: it would fire a
// workspace KV propagation on every apply for unchanged values, and — because
// the server TRIMS display_name — it would silently rename a workspace whose
// stored name happens to carry stray whitespace. pii_redaction is not Computed,
// so its config and plan are identical either way.
func (m *workspaceSettingsResourceModel) updateInput(ctx context.Context) (client.WorkspaceSettingsUpdateInput, diag.Diagnostics) {
	var diags diag.Diagnostics
	in := client.WorkspaceSettingsUpdateInput{
		DisplayName:          strPtr(m.DisplayName),
		EnforceEnabledModels: boolPtr(m.EnforceEnabledModels),
	}
	if m.PiiRedaction == nil {
		return in, diags
	}

	pii := &client.PiiRedaction{Enabled: m.PiiRedaction.Enabled.ValueBool()}
	if cfg := m.PiiRedaction.Config; cfg != nil {
		entities, edia := stringSlice(ctx, cfg.Entities)
		diags.Append(edia...)
		pii.Config = &client.PiiRedactionConfig{
			Language:  strPtr(cfg.Language),
			Entities:  entities,
			OnFailure: strPtr(cfg.OnFailure),
			Threshold: float64Ptr(cfg.Threshold),
		}
	}
	in.PiiRedaction = pii
	return in, diags
}

// applySettings writes the server state onto m.
//
// writePath distinguishes create/update from read, and the two paths have
// OPPOSITE authorities:
//
//   - WRITE (create/update): the PLAN wins for every known value, top-level
//     scalars and the whole pii_redaction subtree alike (preservePlannedPii).
//     The read-back may only fill values the plan left unknown. Anything else
//     risks "Provider produced inconsistent result after apply" — including a
//     managed pii_redaction the response omits entirely, which is kept as
//     planned with a warning rather than nulled.
//   - READ: the SERVER wins, so real out-of-band drift surfaces — including the
//     disappearance of a managed block, which is dropped so the next plan
//     re-creates it.
func applySettings(s *client.WorkspaceSettings, m *workspaceSettingsResourceModel, writePath bool) diag.Diagnostics {
	var diags diag.Diagnostics
	// key is Computed-only, so it always takes the server value.
	m.Key = types.StringValue(s.Key)
	if writePath {
		// Post-apply state must equal the plan for every KNOWN planned value, so a
		// planned display_name / enforce_enabled_models wins over the read-back
		// (mirrors preservePlannedFloat on orq_model). This matters because the
		// write is CONFIG-derived: for an attribute the config leaves out, the plan
		// carries the prior state and the server value is not written — taking the
		// read-back here would break plan consistency whenever the two disagree.
		// The next refresh reconciles state with the server.
		m.DisplayName = preservePlannedString(m.DisplayName, s.DisplayName)
		m.EnforceEnabledModels = preservePlannedBool(m.EnforceEnabledModels, s.EnforceEnabledModels)
	} else {
		// Read: the server is authoritative, so drift surfaces.
		m.DisplayName = types.StringValue(s.DisplayName)
		m.EnforceEnabledModels = types.BoolValue(s.EnforceEnabledModels)
	}

	// Retain-on-null: an UNMANAGED pii_redaction (null in plan/state) is never
	// populated from the server. Importing the server value would both invent a
	// block the operator never wrote and produce a perpetual diff against a
	// config that has none.
	if m.PiiRedaction == nil {
		return diags
	}
	if s.PiiRedaction == nil {
		if writePath {
			diags.AddWarning("PII redaction missing from the read-back",
				"The workspace settings update succeeded but the read-back carried no pii_redaction object. "+
					"The configured block was kept in state; the next refresh will surface the real server value.")
			m.PiiRedaction = preservePlannedPii(m.PiiRedaction, nil)
			return diags
		}
		// Read path: the workspace default was removed out of band. Drop it so
		// the next plan re-creates it from config.
		m.PiiRedaction = nil
		return diags
	}
	if writePath {
		m.PiiRedaction = preservePlannedPii(m.PiiRedaction, s.PiiRedaction)
		return diags
	}
	m.PiiRedaction = applyPii(s.PiiRedaction, m.PiiRedaction)
	return diags
}

// preservePlannedPii is the pii_redaction counterpart of preservePlannedString /
// preservePlannedBool: on the WRITE path every KNOWN planned value wins and the
// read-back may only fill values the plan left UNKNOWN.
//
// Overwriting a known planned nested value with the response is what Terraform
// rejects as "Provider produced inconsistent result after apply", and `entities`
// makes that a live hazard rather than a theoretical one: it is an ordered,
// case-sensitive list, so any difference in order or casing between the request
// and the response would abort the apply. (The server as of orquesta-web
// 4c21d6f4f4 echoes entities verbatim — see the `entities` schema description —
// so in practice there is nothing to differ, but the provider must not depend on
// that to stay correct.)
//
// Unlike the Optional+Computed scalars, the nested attributes here are Optional
// ONLY: a null is a real, deliberate planned value ("this field is not in the
// block") and must be preserved, never refilled from the server. Only an unknown
// — which requires an unresolved reference in the operator's config — falls back
// to the read-back.
func preservePlannedPii(planned *workspaceSettingsPiiModel, srv *client.PiiRedaction) *workspaceSettingsPiiModel {
	if planned == nil {
		return nil
	}
	var srvEnabled bool
	var srvCfg *client.PiiRedactionConfig
	if srv != nil {
		srvEnabled = srv.Enabled
		srvCfg = srv.Config
	}
	out := &workspaceSettingsPiiModel{Enabled: preservePlannedBool(planned.Enabled, srvEnabled)}
	// A config sub-block the plan does not have is never invented from the
	// response, and one the plan does have is never dropped: `config` presence is
	// itself a planned value.
	if planned.Config == nil {
		return out
	}
	if srvCfg == nil {
		srvCfg = &client.PiiRedactionConfig{}
	}
	out.Config = &workspaceSettingsPiiConfigModel{
		Language:  preservePlannedOptString(planned.Config.Language, srvCfg.Language),
		Entities:  preservePlannedEntities(planned.Config.Entities, srvCfg.Entities),
		OnFailure: preservePlannedOptString(planned.Config.OnFailure, srvCfg.OnFailure),
		Threshold: preservePlannedOptFloat(planned.Config.Threshold, srvCfg.Threshold),
	}
	return out
}

// preservePlannedOptString / preservePlannedOptFloat keep ANY known planned
// value — including an explicit null, which for an Optional-only attribute means
// "absent from the block" — and consult the server only for an unknown.
func preservePlannedOptString(planned types.String, srv *string) types.String {
	if !planned.IsUnknown() {
		return planned
	}
	return optStringPtr(srv)
}

func preservePlannedOptFloat(planned types.Float64, srv *float64) types.Float64 {
	if !planned.IsUnknown() {
		return planned
	}
	return optFloat64Ptr(srv)
}

// preservePlannedEntities keeps the planned list verbatim — order and casing
// included — whenever it is fully known. A list that is unknown, or that carries
// an unknown element, cannot be written to state as-is (post-apply state must be
// wholly known), so it is resolved from the read-back through applyEntities.
func preservePlannedEntities(planned types.List, srv []string) types.List {
	if listFullyKnown(planned) {
		return planned
	}
	return applyEntities(planned, srv)
}

// listFullyKnown reports whether l can be written to state unchanged: a null
// list qualifies, an unknown list or one holding any unknown element does not.
func listFullyKnown(l types.List) bool {
	if l.IsUnknown() {
		return false
	}
	if l.IsNull() {
		return true
	}
	for _, e := range l.Elements() {
		if e.IsUnknown() {
			return false
		}
	}
	return true
}

// applyPii projects the server's pii_redaction onto the managed block. cur (the
// prior state) is consulted only to resolve the empty-entities encoding — see
// applyEntities.
//
// READ PATH ONLY. On create/update the plan is authoritative and
// preservePlannedPii is used instead; calling this there would overwrite known
// planned values with the response and break plan consistency.
func applyPii(srv *client.PiiRedaction, cur *workspaceSettingsPiiModel) *workspaceSettingsPiiModel {
	out := &workspaceSettingsPiiModel{Enabled: types.BoolValue(srv.Enabled)}
	if srv.Config == nil {
		// The stored document has no `config` key: the block was written with an
		// enable flag alone (or the config was dropped out of band).
		return out
	}
	curEntities := types.ListNull(types.StringType)
	if cur != nil && cur.Config != nil {
		curEntities = cur.Config.Entities
	}
	out.Config = &workspaceSettingsPiiConfigModel{
		Language:  optStringPtr(srv.Config.Language),
		Entities:  applyEntities(curEntities, srv.Config.Entities),
		OnFailure: optStringPtr(srv.Config.OnFailure),
		Threshold: optFloat64Ptr(srv.Config.Threshold),
	}
	return out
}

// applyEntities resolves the one lossy field in the round trip: the server
// stores NO `entities` key for an empty list (both spellings mean "redact
// everything"), so a read can never tell `entities = []` from an omitted
// attribute. cur — the planned/prior value, which IS authoritative for that
// distinction — decides: a non-null empty config stays `[]`, an absent one stays
// null. A server list always wins outright, so an out-of-band change surfaces.
func applyEntities(cur types.List, srv []string) types.List {
	if len(srv) > 0 {
		return stringListValue(srv)
	}
	if cur.IsUnknown() {
		return types.ListNull(types.StringType)
	}
	if cur.IsNull() {
		return cur
	}
	return stringListValue(nil) // known and empty => [] (non-null)
}

// preservePlannedString / preservePlannedBool keep a KNOWN planned value and
// fall back to the server value only when the plan left the attribute unknown
// (a create with no config value). They exist for the same reason as
// preservePlannedFloat on orq_model: the Create/Update contract requires the
// post-apply state to equal the plan for every known value.
func preservePlannedString(planned types.String, srv string) types.String {
	if !planned.IsNull() && !planned.IsUnknown() {
		return planned
	}
	return types.StringValue(srv)
}

func preservePlannedBool(planned types.Bool, srv bool) types.Bool {
	if !planned.IsNull() && !planned.IsUnknown() {
		return planned
	}
	return types.BoolValue(srv)
}

// writeSettings performs the adopt/update round trip: it PATCHes the fields this
// resource manages and returns the fresh settings. A configuration that manages
// nothing writes nothing — it reads instead, so a read-only use of this resource
// never needs the workspace.update verb and never fires a settings-propagation
// command.
func (r *workspaceSettingsResource) writeSettings(ctx context.Context, in client.WorkspaceSettingsUpdateInput) (*client.WorkspaceSettings, error) {
	if in.IsEmpty() {
		return r.settings.Get(ctx)
	}
	return r.settings.Update(ctx, in)
}

// Create ADOPTS the workspace settings singleton. There is no create RPC (the
// object always exists), so this writes the managed attributes and reads the
// rest back.
func (r *workspaceSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg workspaceSettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	// The CONFIG decides what is written; the plan carries what lands in state.
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, diags := cfg.updateInput(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.writeSettings(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to adopt workspace settings", errDetail(err))
		return
	}
	resp.Diagnostics.Append(applySettings(s, &plan, true)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *workspaceSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workspaceSettingsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.settings.Get(ctx)
	if err != nil {
		if isNotFound(err) {
			// The workspace behind the credential is gone; there is no settings
			// object left to manage.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read workspace settings", errDetail(err))
		return
	}
	resp.Diagnostics.Append(applySettings(s, &state, false)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *workspaceSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg workspaceSettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	// The CONFIG decides what is written; the plan carries what lands in state.
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, diags := cfg.updateInput(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.writeSettings(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to update workspace settings", errDetail(err))
		return
	}
	resp.Diagnostics.Append(applySettings(s, &plan, true)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from Terraform state ONLY. The workspace settings
// singleton cannot be deleted (the API has no delete RPC — a workspace always
// has settings), and reverting the managed attributes to some invented
// "default" would be a destructive guess. Every value stays exactly as last
// applied; the framework drops the resource from state when this returns.
func (r *workspaceSettingsResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Workspace settings left unchanged",
		"orq_workspace_settings was removed from Terraform state, but NOTHING was changed in the workspace: "+
			"the settings singleton cannot be deleted and its values keep whatever was last applied "+
			"(display name, enforce_enabled_models, PII redaction). Change them explicitly if that is not what you want.")
}

// ImportState adopts the singleton. There is no id to parse — the management key
// already selects the workspace — so any import id is accepted and only used to
// seed `key`, which the immediately following Read overwrites with the real
// slug. The documented sentinel is `workspace`:
//
//	terraform import orq_workspace_settings.this workspace
//
// The framework requires import to populate at least one attribute, which is why
// the sentinel is written to state at all.
func (r *workspaceSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), req.ID)...)
}
