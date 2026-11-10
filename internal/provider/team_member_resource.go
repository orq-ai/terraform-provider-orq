package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ resource.Resource                = &teamMemberResource{}
	_ resource.ResourceWithConfigure   = &teamMemberResource{}
	_ resource.ResourceWithImportState = &teamMemberResource{}
)

func NewTeamMemberResource() resource.Resource { return &teamMemberResource{} }

type teamMemberResource struct {
	teams client.TeamsAPI
}

type teamMemberResourceModel struct {
	ID      types.String `tfsdk:"id"`
	TeamID  types.String `tfsdk:"team_id"`
	UserID  types.String `tfsdk:"user_id"`
	Email   types.String `tfsdk:"email"`
	Invited types.Bool   `tfsdk:"invited"`
}

func (r *teamMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_member"
}

func (r *teamMemberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Membership of one workspace user in one team. A membership carries no role: " +
			"what the user can do follows from the team's project access.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Membership identifier, `<team_id>/<user_id>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"team_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Team the user joins. Changing this forces replacement.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					nonEmptyStringValidator{remedy: "Set team_id to the id of an orq_team."},
				},
			},
			"user_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Workspace user ID (a UUID) of the member. This is the workspace membership " +
					"id, not the account id or the email. Changing this forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					nonEmptyStringValidator{remedy: "Set user_id to the id of a workspace user."},
				},
			},
			"email": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Account email of the member, or the invitation email while the user is still invited.",
			},
			"invited": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the user still has an outstanding workspace invitation.",
			},
		},
	}
}

func (r *teamMemberResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.teams = c.Teams()
}

func teamMemberID(teamID, userID string) string {
	return teamID + "/" + userID
}

func parseTeamMemberID(id string) (teamID, userID string, ok bool) {
	teamID, userID, found := strings.Cut(id, "/")
	return teamID, userID, found && teamID != "" && userID != "" && !strings.Contains(userID, "/")
}

func (r *teamMemberResource) apply(member *client.TeamMember, m *teamMemberResourceModel) {
	m.ID = types.StringValue(teamMemberID(m.TeamID.ValueString(), m.UserID.ValueString()))
	m.Email = types.StringValue(member.Email)
	m.Invited = types.BoolValue(member.Invited)
}

func (r *teamMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan teamMemberResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	member, err := r.teams.AddMember(ctx, plan.TeamID.ValueString(), plan.UserID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to add team member", errDetail(err))
		return
	}

	r.apply(member, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *teamMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state teamMemberResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	member, err := r.teams.GetMember(ctx, state.TeamID.ValueString(), state.UserID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read team member", errDetail(err))
		return
	}

	r.apply(member, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable through a plan (both inputs force replacement); adding is idempotent.
func (r *teamMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan teamMemberResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(revalidatePlan(ctx, req.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	member, err := r.teams.AddMember(ctx, plan.TeamID.ValueString(), plan.UserID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to update team member", errDetail(err))
		return
	}

	r.apply(member, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *teamMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state teamMemberResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.teams.RemoveMember(ctx, state.TeamID.ValueString(), state.UserID.ValueString()); err != nil {
		if isNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to remove team member", errDetail(err))
	}
}

func (r *teamMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	teamID, userID, ok := parseTeamMemberID(req.ID)
	if !ok {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected <team_id>/<user_id>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), teamMemberID(teamID, userID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), teamID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), userID)...)
}
