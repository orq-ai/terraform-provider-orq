package client

import (
	"context"
	"fmt"
	"strings"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// Budget scope kinds, as the transport-neutral (prefix-stripped) enum names the
// resource schema accepts. They mirror BudgetScopeKind minus the
// BUDGET_SCOPE_KIND_ prefix.
const (
	BudgetScopeWorkspace = "WORKSPACE"
	BudgetScopeProject   = "PROJECT"
	BudgetScopeIdentity  = "IDENTITY"
	BudgetScopeAPIKey    = "API_KEY"
	BudgetScopeProvider  = "PROVIDER"
	BudgetScopeModel     = "MODEL"
)

// Budget is the transport-agnostic projection of a budget.
type Budget struct {
	ID string

	// Scope is set for scoped budgets; ScopeKind is "" for a custom
	// (match-expression) budget. ScopeTarget is empty for WORKSPACE.
	ScopeKind   string
	ScopeTarget string

	// MatchCEL is the raw matching expression for a custom budget; "" for a
	// scoped budget.
	MatchCEL string

	Period     string // MONTHLY / DAILY / ... ; "" when unset
	Amount     *float64
	TokenLimit *float64
	RateLimit  *int32

	IsActive  bool
	ExpiresAt string // RFC 3339; "" when no expiration
	CreatedAt string
	UpdatedAt string

	Alerts []BudgetAlert
}

// BudgetAlert is a threshold notification on a budget.
type BudgetAlert struct {
	ID               string
	ThresholdPercent int32
	NotifierIDs      []string
	Dimension        string // COST / TOKENS
}

// BudgetPage is one page of a cursor-paginated list.
type BudgetPage struct {
	Budgets []Budget
	HasMore bool
}

// BudgetWriteInput carries the fields for a create or update. Pointers are
// sparse: nil means "leave unchanged" on update / "unset" on create. Exactly
// one of ScopeKind or MatchCEL identifies the budget on create; scope is
// immutable so neither is sent on update.
type BudgetWriteInput struct {
	// Create-only scope selectors (ignored on update).
	ScopeKind   string
	ScopeTarget string
	MatchCEL    *string

	Period     string
	Amount     *float64
	TokenLimit *float64
	RateLimit  *int32
	// SetRateLimit distinguishes "clear the rate limit" from "leave unset".
	// When false, RateLimit is not sent.
	SetRateLimit bool

	IsActive       *bool
	ExpiresAt      string // RFC 3339; "" + ClearExpiresAt=false means unchanged
	ClearExpiresAt bool

	Alerts      []BudgetAlert
	ClearAlerts bool
}

// BudgetsAPI is the per-resource seam for the budgets domain (Connect-backed).
type BudgetsAPI interface {
	List(ctx context.Context, params ListParams) (*BudgetPage, error)
	Get(ctx context.Context, id string) (*Budget, error)
	Create(ctx context.Context, in BudgetWriteInput) (*Budget, error)
	Update(ctx context.Context, id string, in BudgetWriteInput) (*Budget, error)
	Delete(ctx context.Context, id string) error
}

type connectBudgets struct {
	c platformv1connect.BudgetsServiceClient
}

// --- enum helpers ---------------------------------------------------------

func budgetPeriodToProto(short string) (platformv1.BudgetPeriod, error) {
	if short == "" {
		return platformv1.BudgetPeriod_BUDGET_PERIOD_UNSPECIFIED, nil
	}
	v, ok := platformv1.BudgetPeriod_value["BUDGET_PERIOD_"+strings.ToUpper(short)]
	if !ok {
		return 0, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown budget period %q", short)}
	}
	return platformv1.BudgetPeriod(v), nil
}

func budgetPeriodFromProto(p platformv1.BudgetPeriod) string {
	if p == platformv1.BudgetPeriod_BUDGET_PERIOD_UNSPECIFIED {
		return ""
	}
	return strings.TrimPrefix(p.String(), "BUDGET_PERIOD_")
}

func budgetDimensionToProto(short string) (platformv1.BudgetAlertDimension, error) {
	if short == "" {
		return platformv1.BudgetAlertDimension_BUDGET_ALERT_DIMENSION_UNSPECIFIED, nil
	}
	v, ok := platformv1.BudgetAlertDimension_value["BUDGET_ALERT_DIMENSION_"+strings.ToUpper(short)]
	if !ok {
		return 0, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown budget alert dimension %q", short)}
	}
	return platformv1.BudgetAlertDimension(v), nil
}

func budgetDimensionFromProto(d platformv1.BudgetAlertDimension) string {
	if d == platformv1.BudgetAlertDimension_BUDGET_ALERT_DIMENSION_UNSPECIFIED {
		return BudgetDimensionCost // server treats UNSPECIFIED as COST
	}
	return strings.TrimPrefix(d.String(), "BUDGET_ALERT_DIMENSION_")
}

// BudgetDimensionCost is the default alert dimension.
const BudgetDimensionCost = "COST"

// scopeToProto builds the BudgetScope oneof from the kind + target.
func scopeToProto(kind, target string) (*platformv1.BudgetScope, error) {
	switch strings.ToUpper(kind) {
	case BudgetScopeWorkspace:
		return &platformv1.BudgetScope{Kind: &platformv1.BudgetScope_Workspace{Workspace: &platformv1.WorkspaceBudgetScope{}}}, nil
	case BudgetScopeProject:
		return &platformv1.BudgetScope{Kind: &platformv1.BudgetScope_Project{Project: &platformv1.ProjectBudgetScope{ProjectId: target}}}, nil
	case BudgetScopeIdentity:
		return &platformv1.BudgetScope{Kind: &platformv1.BudgetScope_Identity{Identity: &platformv1.IdentityBudgetScope{IdentityExternalId: target}}}, nil
	case BudgetScopeAPIKey:
		return &platformv1.BudgetScope{Kind: &platformv1.BudgetScope_ApiKey{ApiKey: &platformv1.ApiKeyBudgetScope{ApiKeyId: target}}}, nil
	case BudgetScopeProvider:
		return &platformv1.BudgetScope{Kind: &platformv1.BudgetScope_Provider{Provider: &platformv1.ProviderBudgetScope{Provider: target}}}, nil
	case BudgetScopeModel:
		return &platformv1.BudgetScope{Kind: &platformv1.BudgetScope_Model{Model: &platformv1.ModelBudgetScope{ModelId: target}}}, nil
	default:
		return nil, fmt.Errorf("unknown budget scope kind %q", kind)
	}
}

// scopeFromProto reads the oneof back into (kind, target).
func scopeFromProto(s *platformv1.BudgetScope) (kind, target string) {
	if s == nil {
		return "", ""
	}
	switch {
	case s.GetWorkspace() != nil:
		return BudgetScopeWorkspace, ""
	case s.GetProject() != nil:
		return BudgetScopeProject, s.GetProject().GetProjectId()
	case s.GetIdentity() != nil:
		return BudgetScopeIdentity, s.GetIdentity().GetIdentityExternalId()
	case s.GetApiKey() != nil:
		return BudgetScopeAPIKey, s.GetApiKey().GetApiKeyId()
	case s.GetProvider() != nil:
		return BudgetScopeProvider, s.GetProvider().GetProvider()
	case s.GetModel() != nil:
		return BudgetScopeModel, s.GetModel().GetModelId()
	default:
		return "", ""
	}
}

func budgetFromProto(b *platformv1.Budget) (Budget, error) {
	kind, target := scopeFromProto(b.GetScope())
	out := Budget{
		ID:          b.GetBudgetId(),
		ScopeKind:   kind,
		ScopeTarget: target,
		IsActive:    b.GetIsActive(),
		ExpiresAt:   formatTimestamp(b.GetExpiresAt()),
		CreatedAt:   formatTimestamp(b.GetCreatedAt()),
		UpdatedAt:   formatTimestamp(b.GetUpdatedAt()),
	}
	if m := b.GetMatch(); m != nil {
		out.MatchCEL = m.GetCel()
	}
	if l := b.GetLimits(); l != nil {
		out.Period = budgetPeriodFromProto(l.GetPeriod())
		if l.Amount != nil {
			v := l.GetAmount()
			out.Amount = &v
		}
		if l.TokenLimit != nil {
			v := l.GetTokenLimit()
			out.TokenLimit = &v
		}
	}
	if rl := b.GetRateLimit(); rl != nil && rl.RequestsPerMinute != nil {
		v := rl.GetRequestsPerMinute()
		out.RateLimit = &v
	}
	for _, a := range b.GetAlerts() {
		out.Alerts = append(out.Alerts, BudgetAlert{
			ID:               a.GetId(),
			ThresholdPercent: int32(a.GetThresholdPercent()),
			NotifierIDs:      a.GetNotifierIds(),
			Dimension:        budgetDimensionFromProto(a.GetDimension()),
		})
	}
	return out, nil
}

func (in BudgetWriteInput) alertsToProto() ([]*platformv1.BudgetAlert, error) {
	var out []*platformv1.BudgetAlert
	for _, a := range in.Alerts {
		dim, err := budgetDimensionToProto(a.Dimension)
		if err != nil {
			return nil, err
		}
		pa := &platformv1.BudgetAlert{
			ThresholdPercent: uint32(a.ThresholdPercent),
			NotifierIds:      a.NotifierIDs,
			Dimension:        dim,
		}
		if a.ID != "" {
			pa.Id = a.ID
		}
		out = append(out, pa)
	}
	return out, nil
}

func (in BudgetWriteInput) limitsToProto() (*platformv1.BudgetLimits, error) {
	period, err := budgetPeriodToProto(in.Period)
	if err != nil {
		return nil, err
	}
	l := &platformv1.BudgetLimits{Period: period}
	if in.Amount != nil {
		l.Amount = in.Amount
	}
	if in.TokenLimit != nil {
		l.TokenLimit = in.TokenLimit
	}
	return l, nil
}

func (c *connectBudgets) List(ctx context.Context, params ListParams) (*BudgetPage, error) {
	req := &platformv1.ListBudgetsRequest{}
	if params.Limit > 0 {
		req.Limit = &params.Limit
	}
	if params.StartingAfter != "" {
		req.StartingAfter = params.StartingAfter
	}
	resp, err := c.c.ListBudgets(ctx, req)
	if err != nil {
		return nil, mapConnectError("budget", err)
	}
	out := &BudgetPage{HasMore: resp.GetHasMore()}
	for _, b := range resp.GetData() {
		bb, err := budgetFromProto(b)
		if err != nil {
			return nil, err
		}
		out.Budgets = append(out.Budgets, bb)
	}
	return out, nil
}

func (c *connectBudgets) Get(ctx context.Context, id string) (*Budget, error) {
	resp, err := c.c.GetBudget(ctx, &platformv1.GetBudgetRequest{BudgetId: id})
	if err != nil {
		return nil, mapConnectError("budget", err)
	}
	b, err := budgetFromProto(resp.GetBudget())
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (c *connectBudgets) Create(ctx context.Context, in BudgetWriteInput) (*Budget, error) {
	limits, err := in.limitsToProto()
	if err != nil {
		return nil, err
	}
	req := &platformv1.CreateBudgetRequest{Limits: limits}

	if in.MatchCEL != nil {
		req.Match = &platformv1.BudgetMatch{Cel: *in.MatchCEL}
	} else {
		scope, err := scopeToProto(in.ScopeKind, in.ScopeTarget)
		if err != nil {
			return nil, err
		}
		req.Scope = scope
	}
	if in.SetRateLimit && in.RateLimit != nil {
		req.RateLimit = &platformv1.RateLimit{RequestsPerMinute: in.RateLimit}
	}
	if in.IsActive != nil {
		req.IsActive = in.IsActive
	}
	if in.ExpiresAt != "" {
		ts, err := parseTimestamp(in.ExpiresAt)
		if err != nil {
			return nil, &Error{Code: CodeInvalid, Message: "invalid expires_at: " + err.Error()}
		}
		req.ExpiresAt = ts
	}
	alerts, err := in.alertsToProto()
	if err != nil {
		return nil, err
	}
	req.Alerts = alerts

	resp, err := c.c.CreateBudget(ctx, req)
	if err != nil {
		return nil, mapConnectError("budget", err)
	}
	b, err := budgetFromProto(resp.GetBudget())
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (c *connectBudgets) Update(ctx context.Context, id string, in BudgetWriteInput) (*Budget, error) {
	req := &platformv1.UpdateBudgetRequest{BudgetId: id}

	limits, err := in.limitsToProto()
	if err != nil {
		return nil, err
	}
	req.Limits = limits

	if in.SetRateLimit {
		req.RateLimit = &platformv1.RateLimit{}
		if in.RateLimit != nil {
			req.RateLimit.RequestsPerMinute = in.RateLimit
		}
	}
	if in.MatchCEL != nil {
		req.Match = &platformv1.BudgetMatch{Cel: *in.MatchCEL}
	}
	if in.IsActive != nil {
		req.IsActive = in.IsActive
	}
	if in.ClearExpiresAt {
		req.ClearExpiresAt = true
	} else if in.ExpiresAt != "" {
		ts, err := parseTimestamp(in.ExpiresAt)
		if err != nil {
			return nil, &Error{Code: CodeInvalid, Message: "invalid expires_at: " + err.Error()}
		}
		req.ExpiresAt = ts
	}
	if in.ClearAlerts {
		req.ClearAlerts = true
	} else {
		alerts, err := in.alertsToProto()
		if err != nil {
			return nil, err
		}
		req.Alerts = alerts
	}

	resp, err := c.c.UpdateBudget(ctx, req)
	if err != nil {
		return nil, mapConnectError("budget", err)
	}
	b, err := budgetFromProto(resp.GetBudget())
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (c *connectBudgets) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteBudget(ctx, &platformv1.DeleteBudgetRequest{BudgetId: id})
	return mapConnectError("budget", err)
}
