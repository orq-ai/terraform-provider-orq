package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type fakeBudgetsHandler struct {
	platformv1connect.UnimplementedBudgetsServiceHandler
	lastCreate *platformv1.CreateBudgetRequest
	lastUpdate *platformv1.UpdateBudgetRequest
	budget     *platformv1.Budget
}

func (f *fakeBudgetsHandler) CreateBudget(_ context.Context, req *platformv1.CreateBudgetRequest) (*platformv1.CreateBudgetResponse, error) {
	f.lastCreate = req
	return &platformv1.CreateBudgetResponse{Budget: f.budget}, nil
}
func (f *fakeBudgetsHandler) GetBudget(_ context.Context, _ *platformv1.GetBudgetRequest) (*platformv1.GetBudgetResponse, error) {
	return &platformv1.GetBudgetResponse{Budget: f.budget}, nil
}
func (f *fakeBudgetsHandler) UpdateBudget(_ context.Context, req *platformv1.UpdateBudgetRequest) (*platformv1.UpdateBudgetResponse, error) {
	f.lastUpdate = req
	return &platformv1.UpdateBudgetResponse{Budget: f.budget}, nil
}
func (f *fakeBudgetsHandler) DeleteBudget(_ context.Context, _ *platformv1.DeleteBudgetRequest) (*platformv1.DeleteBudgetResponse, error) {
	return &platformv1.DeleteBudgetResponse{}, nil
}
func (f *fakeBudgetsHandler) ListBudgets(_ context.Context, _ *platformv1.ListBudgetsRequest) (*platformv1.ListBudgetsResponse, error) {
	return &platformv1.ListBudgetsResponse{Object: "list", Data: []*platformv1.Budget{f.budget}, HasMore: false}, nil
}

func newBudgetsClient(t *testing.T, h *fakeBudgetsHandler) *Client {
	t.Helper()
	path, handler := platformv1connect.NewBudgetsServiceHandler(h)
	mux := http.NewServeMux()
	mux.Handle("/v3/rpc/platform"+path, http.StripPrefix("/v3/rpc/platform", handler))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func sampleBudget() *platformv1.Budget {
	amount := 1000.0
	return &platformv1.Budget{
		BudgetId: "budget_1",
		Scope: &platformv1.BudgetScope{
			Kind: &platformv1.BudgetScope_Project{Project: &platformv1.ProjectBudgetScope{ProjectId: "proj_9"}},
		},
		Limits:   &platformv1.BudgetLimits{Period: platformv1.BudgetPeriod_BUDGET_PERIOD_MONTHLY, Amount: &amount},
		IsActive: true,
		Alerts: []*platformv1.BudgetAlert{
			{Id: "al_1", ThresholdPercent: 80, NotifierIds: []string{"nf_1"}, Dimension: platformv1.BudgetAlertDimension_BUDGET_ALERT_DIMENSION_COST},
		},
	}
}

func TestBudgets_CreateScopeMapping(t *testing.T) {
	h := &fakeBudgetsHandler{budget: sampleBudget()}
	c := newBudgetsClient(t, h)

	amount := 1000.0
	b, err := c.Budgets().Create(context.Background(), BudgetWriteInput{
		ScopeKind:   BudgetScopeProject,
		ScopeTarget: "proj_9",
		Period:      "MONTHLY",
		Amount:      &amount,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The scope oneof must be wired through as project/proj_9.
	if h.lastCreate.GetScope().GetProject().GetProjectId() != "proj_9" {
		t.Errorf("scope not mapped: %+v", h.lastCreate.GetScope())
	}
	if h.lastCreate.GetLimits().GetPeriod() != platformv1.BudgetPeriod_BUDGET_PERIOD_MONTHLY {
		t.Errorf("period not mapped: %v", h.lastCreate.GetLimits().GetPeriod())
	}
	// Read-back projection.
	if b.ScopeKind != BudgetScopeProject || b.ScopeTarget != "proj_9" {
		t.Errorf("scope read-back wrong: %q/%q", b.ScopeKind, b.ScopeTarget)
	}
	if b.Period != "MONTHLY" || b.Amount == nil || *b.Amount != 1000.0 {
		t.Errorf("limits read-back wrong: period=%q amount=%v", b.Period, b.Amount)
	}
	if len(b.Alerts) != 1 || b.Alerts[0].Dimension != "COST" || b.Alerts[0].ThresholdPercent != 80 {
		t.Errorf("alert read-back wrong: %+v", b.Alerts)
	}
}

func TestBudgets_CreateMatchCEL(t *testing.T) {
	h := &fakeBudgetsHandler{budget: sampleBudget()}
	c := newBudgetsClient(t, h)
	cel := `provider == "openai"`
	if _, err := c.Budgets().Create(context.Background(), BudgetWriteInput{MatchCEL: &cel, Period: "DAILY"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.lastCreate.GetMatch().GetCel() != cel {
		t.Errorf("match CEL not sent: %q", h.lastCreate.GetMatch().GetCel())
	}
	if h.lastCreate.GetScope() != nil {
		t.Errorf("scope should be nil for a match budget, got %+v", h.lastCreate.GetScope())
	}
}

func TestBudgets_UpdateClearsAndRateLimit(t *testing.T) {
	h := &fakeBudgetsHandler{budget: sampleBudget()}
	c := newBudgetsClient(t, h)
	rpm := int32(60)
	_, err := c.Budgets().Update(context.Background(), "budget_1", BudgetWriteInput{
		Period:         "MONTHLY",
		RateLimit:      &rpm,
		SetRateLimit:   true,
		ClearExpiresAt: true,
		ClearAlerts:    true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if h.lastUpdate.GetRateLimit().GetRequestsPerMinute() != 60 {
		t.Errorf("rate limit not sent: %v", h.lastUpdate.GetRateLimit())
	}
	if !h.lastUpdate.GetClearExpiresAt() || !h.lastUpdate.GetClearAlerts() {
		t.Errorf("clear flags not set: expires=%v alerts=%v", h.lastUpdate.GetClearExpiresAt(), h.lastUpdate.GetClearAlerts())
	}
}

func TestBudgets_Delete(t *testing.T) {
	h := &fakeBudgetsHandler{budget: sampleBudget()}
	c := newBudgetsClient(t, h)
	if err := c.Budgets().Delete(context.Background(), "budget_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
