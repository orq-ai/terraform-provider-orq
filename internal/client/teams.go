package client

import (
	"context"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type Team struct {
	ID          string
	DisplayName string
	Description string
	AdminGroup  bool
	CreatedAt   string
	UpdatedAt   string
}

type TeamCreateInput struct {
	DisplayName string
	Description string
}

type TeamUpdateInput struct {
	ID          string
	DisplayName *string
	Description *string
}

type TeamMember struct {
	TeamID      string
	UserID      string
	AccountID   string
	Email       string
	DisplayName string
	Invited     bool
}

type TeamsAPI interface {
	Get(ctx context.Context, id string) (*Team, error)
	Create(ctx context.Context, in TeamCreateInput) (*Team, error)
	Update(ctx context.Context, in TeamUpdateInput) (*Team, error)
	Delete(ctx context.Context, id string) error

	GetMember(ctx context.Context, teamID, userID string) (*TeamMember, error)
	AddMember(ctx context.Context, teamID, userID string) (*TeamMember, error)
	RemoveMember(ctx context.Context, teamID, userID string) error
}

type connectTeams struct {
	c platformv1connect.TeamsServiceClient
}

func teamFromProto(t *platformv1.Team) *Team {
	return &Team{
		ID:          t.GetId(),
		DisplayName: t.GetDisplayName(),
		Description: t.GetDescription(),
		AdminGroup:  t.GetAdminGroup(),
		CreatedAt:   formatTimestamp(t.GetCreated()),
		UpdatedAt:   formatTimestamp(t.GetUpdated()),
	}
}

func teamMemberFromProto(teamID string, u *platformv1.TeamUser) *TeamMember {
	return &TeamMember{
		TeamID:      teamID,
		UserID:      u.GetUserId(),
		AccountID:   u.GetAccountId(),
		Email:       u.GetEmail(),
		DisplayName: u.GetDisplayName(),
		Invited:     u.GetInvited(),
	}
}

func (c *connectTeams) Get(ctx context.Context, id string) (*Team, error) {
	resp, err := c.c.GetTeam(ctx, &platformv1.GetTeamRequest{TeamId: id})
	if err != nil {
		return nil, mapConnectError("team", err)
	}
	return teamFromProto(resp.GetTeam()), nil
}

func (c *connectTeams) Create(ctx context.Context, in TeamCreateInput) (*Team, error) {
	resp, err := c.c.CreateTeam(ctx, &platformv1.CreateTeamRequest{
		DisplayName: in.DisplayName,
		Description: in.Description,
	})
	if err != nil {
		return nil, mapConnectError("team", err)
	}
	return teamFromProto(resp.GetTeam()), nil
}

// Update never sends user_ids or project_ids: the server replaces those sets when present.
func (c *connectTeams) Update(ctx context.Context, in TeamUpdateInput) (*Team, error) {
	resp, err := c.c.UpdateTeam(ctx, &platformv1.UpdateTeamRequest{
		TeamId:      in.ID,
		DisplayName: in.DisplayName,
		Description: in.Description,
	})
	if err != nil {
		return nil, mapConnectError("team", err)
	}
	return teamFromProto(resp.GetTeam()), nil
}

func (c *connectTeams) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteTeam(ctx, &platformv1.DeleteTeamRequest{TeamId: id})
	return mapConnectError("team", err)
}

func (c *connectTeams) GetMember(ctx context.Context, teamID, userID string) (*TeamMember, error) {
	resp, err := c.c.GetTeamUser(ctx, &platformv1.GetTeamUserRequest{TeamId: teamID, UserId: userID})
	if err != nil {
		return nil, mapConnectError("team member", err)
	}
	return teamMemberFromProto(teamID, resp.GetUser()), nil
}

func (c *connectTeams) AddMember(ctx context.Context, teamID, userID string) (*TeamMember, error) {
	resp, err := c.c.AddTeamUser(ctx, &platformv1.AddTeamUserRequest{TeamId: teamID, UserId: userID})
	if err != nil {
		return nil, mapConnectError("team member", err)
	}
	return teamMemberFromProto(teamID, resp.GetUser()), nil
}

func (c *connectTeams) RemoveMember(ctx context.Context, teamID, userID string) error {
	_, err := c.c.RemoveTeamUser(ctx, &platformv1.RemoveTeamUserRequest{TeamId: teamID, UserId: userID})
	return mapConnectError("team member", err)
}
