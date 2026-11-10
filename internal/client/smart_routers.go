package client

import (
	"context"
	"fmt"
	"strings"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

const smartRouterProfilePrefix = "SMART_ROUTER_PROFILE_"

var SmartRouterProfiles = []string{"quality", "balanced", "cost"}

type SmartRouter struct {
	ID              string
	Key             string
	ModelRef        string
	Models          []string
	Profile         string
	Enabled         bool
	SessionAffinity *SmartRouterSessionAffinity
	CreatedAt       string
	UpdatedAt       string
}

type SmartRouterSessionAffinity struct {
	Enabled    bool
	TTLSeconds *int64
}

type SmartRouterCreateInput struct {
	Key             string
	Models          []string
	Profile         string
	SessionAffinity *SmartRouterSessionAffinity
}

// A nil SessionAffinity keeps the stored settings; the API has no way to unset them.
type SmartRouterUpdateInput struct {
	ID              string
	Models          []string
	Profile         string
	SessionAffinity *SmartRouterSessionAffinity
}

type SmartRoutersAPI interface {
	Get(ctx context.Context, id string) (*SmartRouter, error)
	Create(ctx context.Context, in SmartRouterCreateInput) (*SmartRouter, error)
	Update(ctx context.Context, in SmartRouterUpdateInput) (*SmartRouter, error)
	SetEnabled(ctx context.Context, id string, enabled bool) (*SmartRouter, error)
	Delete(ctx context.Context, id string) error
}

type connectSmartRouters struct {
	c platformv1connect.SmartRoutersServiceClient
}

func smartRouterProfileToProto(profile string) (platformv1.SmartRouterProfile, error) {
	value, ok := platformv1.SmartRouterProfile_value[smartRouterProfilePrefix+strings.ToUpper(profile)]
	if !ok || value == 0 {
		return 0, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown smart router profile %q", profile)}
	}
	return platformv1.SmartRouterProfile(value), nil
}

func smartRouterProfileFromProto(profile platformv1.SmartRouterProfile) string {
	return strings.ToLower(strings.TrimPrefix(profile.String(), smartRouterProfilePrefix))
}

func sessionAffinityToProto(affinity *SmartRouterSessionAffinity) *platformv1.SmartRouterSessionAffinity {
	if affinity == nil {
		return nil
	}
	out := &platformv1.SmartRouterSessionAffinity{Enabled: affinity.Enabled}
	if affinity.TTLSeconds != nil {
		ttlSeconds := int32(*affinity.TTLSeconds)
		out.TtlSeconds = &ttlSeconds
	}
	return out
}

func sessionAffinityFromProto(affinity *platformv1.SmartRouterSessionAffinity) *SmartRouterSessionAffinity {
	if affinity == nil {
		return nil
	}
	out := &SmartRouterSessionAffinity{Enabled: affinity.GetEnabled()}
	if affinity.TtlSeconds != nil {
		ttlSeconds := int64(affinity.GetTtlSeconds())
		out.TTLSeconds = &ttlSeconds
	}
	return out
}

func smartRouterFromProto(g *platformv1.SmartRouter) *SmartRouter {
	return &SmartRouter{
		ID:              g.GetSmartRouterId(),
		Key:             g.GetKey(),
		ModelRef:        g.GetModelRef(),
		Models:          g.GetModels(),
		Profile:         smartRouterProfileFromProto(g.GetProfile()),
		Enabled:         g.GetEnabled(),
		SessionAffinity: sessionAffinityFromProto(g.GetSessionAffinity()),
		CreatedAt:       formatTimestamp(g.GetCreatedAt()),
		UpdatedAt:       formatTimestamp(g.GetUpdatedAt()),
	}
}

func (c *connectSmartRouters) Get(ctx context.Context, id string) (*SmartRouter, error) {
	resp, err := c.c.GetSmartRouter(ctx, &platformv1.GetSmartRouterRequest{SmartRouterId: id})
	if err != nil {
		return nil, mapConnectError("smart router", err)
	}
	return smartRouterFromProto(resp.GetSmartRouter()), nil
}

func (c *connectSmartRouters) Create(ctx context.Context, in SmartRouterCreateInput) (*SmartRouter, error) {
	profile, err := smartRouterProfileToProto(in.Profile)
	if err != nil {
		return nil, err
	}
	resp, err := c.c.CreateSmartRouter(ctx, &platformv1.CreateSmartRouterRequest{
		Key:             in.Key,
		Models:          in.Models,
		Profile:         profile,
		SessionAffinity: sessionAffinityToProto(in.SessionAffinity),
	})
	if err != nil {
		return nil, mapConnectError("smart router", err)
	}
	return smartRouterFromProto(resp.GetSmartRouter()), nil
}

func (c *connectSmartRouters) Update(ctx context.Context, in SmartRouterUpdateInput) (*SmartRouter, error) {
	profile, err := smartRouterProfileToProto(in.Profile)
	if err != nil {
		return nil, err
	}
	resp, err := c.c.UpdateSmartRouter(ctx, &platformv1.UpdateSmartRouterRequest{
		SmartRouterId:   in.ID,
		Models:          in.Models,
		Profile:         &profile,
		SessionAffinity: sessionAffinityToProto(in.SessionAffinity),
	})
	if err != nil {
		return nil, mapConnectError("smart router", err)
	}
	return smartRouterFromProto(resp.GetSmartRouter()), nil
}

func (c *connectSmartRouters) SetEnabled(ctx context.Context, id string, enabled bool) (*SmartRouter, error) {
	resp, err := c.c.SetSmartRouterEnabled(ctx, &platformv1.SetSmartRouterEnabledRequest{SmartRouterId: id, Enabled: enabled})
	if err != nil {
		return nil, mapConnectError("smart router", err)
	}
	return smartRouterFromProto(resp.GetSmartRouter()), nil
}

func (c *connectSmartRouters) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteSmartRouter(ctx, &platformv1.DeleteSmartRouterRequest{SmartRouterId: id})
	return mapConnectError("smart router", err)
}
