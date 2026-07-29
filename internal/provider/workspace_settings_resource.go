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
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                = &workspaceSettingsResource{}
	_ resource.ResourceWithConfigure   = &workspaceSettingsResource{}
	_ resource.ResourceWithImportState = &workspaceSettingsResource{}
)

var (
	piiLanguages    = []string{"en", "nl"}
	piiFailureModes = []string{"block", "passthrough"}
)

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
					stringvalidator.UTF8LengthBetween(1, 128),
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
				// Optional but deliberately NOT Computed: Computed would resolve an
				// omitted block to the server value, conflating "unmanaged" with
				// "managed as whatever is stored".
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

// displayNameWhitespaceValidator uses strings.TrimSpace rather than a regexp
// because Go's `\S` is ASCII-only: it would accept a name padded with U+00A0 or
// U+2003 that the server's Unicode-aware trim strips.
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

// --- CRUD --------------------------------------------------------------------

// Create ADOPTS the settings singleton: there is no create RPC, so it writes the
// managed attributes and reads the rest back.
func (r *workspaceSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.Append(r.adoptOrUpdate(ctx, req.Plan, req.Config, &resp.State, "Unable to adopt workspace settings")...)
}

func (r *workspaceSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.Append(r.adoptOrUpdate(ctx, req.Plan, req.Config, &resp.State, "Unable to update workspace settings")...)
}

func (r *workspaceSettingsResource) adoptOrUpdate(ctx context.Context, plan tfsdk.Plan, config tfsdk.Config, state *tfsdk.State, errSummary string) diag.Diagnostics {
	var diags diag.Diagnostics
	var planned, cfg workspaceSettingsResourceModel
	diags.Append(plan.Get(ctx, &planned)...)
	// The CONFIG decides what is written; the plan carries what lands in state.
	diags.Append(config.Get(ctx, &cfg)...)
	if diags.HasError() {
		return diags
	}
	in, inDiags := cfg.updateInput(ctx)
	diags.Append(inDiags...)
	if diags.HasError() {
		return diags
	}
	s, err := r.writeSettings(ctx, in)
	if err != nil {
		diags.AddError(errSummary, errDetail(err))
		return diags
	}
	diags.Append(applyWrittenSettings(s, &planned)...)
	diags.Append(state.Set(ctx, &planned)...)
	return diags
}

func (r *workspaceSettingsResource) writeSettings(ctx context.Context, in client.WorkspaceSettingsUpdateInput) (*client.WorkspaceSettings, error) {
	if in.IsEmpty() {
		return r.settings.Get(ctx)
	}
	return r.settings.Update(ctx, in)
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
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read workspace settings", errDetail(err))
		return
	}
	applyReadSettings(s, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete removes the resource from Terraform state ONLY: the singleton has no
// delete RPC and reverting to an invented default would be a destructive guess.
func (r *workspaceSettingsResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Workspace settings left unchanged",
		"orq_workspace_settings was removed from Terraform state, but NOTHING was changed in the workspace: "+
			"the settings singleton cannot be deleted and its values keep whatever was last applied "+
			"(display name, enforce_enabled_models, PII redaction). Change them explicitly if that is not what you want.")
}

// ImportState accepts any id (the documented sentinel is `workspace`): the
// management key already selects the workspace. The id only seeds `key`, which
// the framework requires import to populate and the following Read overwrites.
func (r *workspaceSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), req.ID)...)
}

// --- config -> request -------------------------------------------------------

// updateInput builds the partial-update payload from the CONFIG. A null/unknown
// attribute yields a nil pointer, which the client omits from the request.
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

// --- server -> state ---------------------------------------------------------

// applyWrittenSettings assembles post-apply state: the PLAN wins for every known
// value and the read-back may only fill what the plan left unknown.
func applyWrittenSettings(s *client.WorkspaceSettings, m *workspaceSettingsResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	m.Key = types.StringValue(s.Key)
	m.DisplayName = preservePlannedString(m.DisplayName, s.DisplayName)
	m.EnforceEnabledModels = preservePlannedBool(m.EnforceEnabledModels, s.EnforceEnabledModels)

	if m.PiiRedaction == nil {
		return diags
	}
	if s.PiiRedaction == nil {
		diags.AddWarning("PII redaction missing from the read-back",
			"The workspace settings update succeeded but the read-back carried no pii_redaction object. "+
				"The configured block was kept in state; the next refresh will surface the real server value.")
	}
	m.PiiRedaction = preservePlannedPii(m.PiiRedaction, s.PiiRedaction)
	return diags
}

// applyReadSettings refreshes state from the server, which wins outright so that
// out-of-band drift surfaces. An UNMANAGED pii_redaction (null in state) is never
// populated: importing it would invent a block the operator never wrote.
func applyReadSettings(s *client.WorkspaceSettings, m *workspaceSettingsResourceModel) {
	m.Key = types.StringValue(s.Key)
	m.DisplayName = types.StringValue(s.DisplayName)
	m.EnforceEnabledModels = types.BoolValue(s.EnforceEnabledModels)

	if m.PiiRedaction == nil {
		return
	}
	if s.PiiRedaction == nil {
		m.PiiRedaction = nil
		return
	}
	m.PiiRedaction = applyPii(s.PiiRedaction, m.PiiRedaction)
}

// applyPii projects the server's pii_redaction onto the managed block. cur (the
// prior state) is consulted only to resolve the empty-entities encoding.
func applyPii(srv *client.PiiRedaction, cur *workspaceSettingsPiiModel) *workspaceSettingsPiiModel {
	out := &workspaceSettingsPiiModel{Enabled: types.BoolValue(srv.Enabled)}
	if srv.Config == nil {
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
	// `config` presence is itself a planned value: never invented, never dropped.
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

// preservePlannedOptString / preservePlannedOptFloat differ from their top-level
// counterparts on purpose: the nested pii attributes are Optional but NOT
// Computed, so a planned null is a real value ("absent from the block") and must
// be preserved rather than refilled from the server.
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

func preservePlannedEntities(planned types.List, srv []string) types.List {
	if listFullyKnown(planned) {
		return planned
	}
	return applyEntities(planned, srv)
}

// listFullyKnown reports whether l can be written to post-apply state unchanged:
// a null list qualifies, an unknown list or one holding an unknown element does
// not.
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

// applyEntities resolves the one lossy field in the round trip: the server stores
// NO `entities` key for an empty list, so a read cannot tell `entities = []` from
// an omitted attribute. cur — the planned/prior value — decides.
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
	return stringListValue(nil)
}

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
