package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/store"
	"go.uber.org/zap"
)

func TestCompleteCheckout_ActivatesSubscription(t *testing.T) {
	fake := newFakePersistence()
	fake.checkouts["chk_1"] = store.CheckoutSession{
		ID:        "chk_1",
		UserID:    "u1",
		PlanID:    "pro-monthly",
		Provider:  checkoutProviderMock,
		Status:    checkoutStatusPending,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	h := &Handler{log: zap.NewNop(), db: fake}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/billing/checkout/:id/complete", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.CompleteCheckout(c)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/billing/checkout/chk_1/complete", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body subscriptionResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.PlanID != "pro-monthly" || body.Status != "active" || body.CurrentPeriodEnd == nil {
		t.Fatalf("subscription response mismatch: %+v", body)
	}
	if got := fake.checkouts["chk_1"].Status; got != "completed" {
		t.Fatalf("checkout status = %q, want completed", got)
	}
}

func TestCompleteCheckout_CompletedSessionIsIdempotentAfterExpiry(t *testing.T) {
	fake := newFakePersistence()
	fake.checkouts["chk_done"] = store.CheckoutSession{
		ID:        "chk_done",
		UserID:    "u1",
		PlanID:    "team-monthly",
		Provider:  checkoutProviderMock,
		Status:    "completed",
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	h := &Handler{log: zap.NewNop(), db: fake}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/billing/checkout/:id/complete", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.CompleteCheckout(c)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/billing/checkout/chk_done/complete", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected idempotent 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got := fake.checkouts["chk_done"].Status; got != "completed" {
		t.Fatalf("completed checkout must not regress to %q", got)
	}
	if got := fake.subscriptions["u1"].PlanID; got != "team-monthly" {
		t.Fatalf("subscription plan = %q, want team-monthly", got)
	}
}
