package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func billingRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", "alice")
		c.Next()
	})
	r.GET("/billing/plans", h.GetBillingPlans)
	r.GET("/me/subscription", h.GetMySubscription)
	r.POST("/billing/checkout", h.CreateCheckout)
	return r
}

func TestBillingPlans(t *testing.T) {
	h := &Handler{log: zap.NewNop()}
	r := billingRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/billing/plans", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Items []billingPlan `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) < 2 {
		t.Fatalf("expected multiple plans, got %+v", body.Items)
	}
}

func TestMySubscriptionDefaultsToFree(t *testing.T) {
	db := newFakePersistence()
	h := &Handler{log: zap.NewNop(), db: db}
	r := billingRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me/subscription", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body subscriptionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.PlanID != defaultPlanID || body.Status != "free" {
		t.Fatalf("expected default free subscription, got %+v", body)
	}
}

func TestCreateCheckoutStoresMockSession(t *testing.T) {
	db := newFakePersistence()
	h := &Handler{log: zap.NewNop(), db: db}
	r := billingRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(
		http.MethodPost,
		"/billing/checkout",
		bytes.NewBufferString(`{"plan_id":"pro-monthly"}`),
	))

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body checkoutResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Provider != checkoutProviderMock || body.Status != checkoutStatusPending || body.CheckoutURL == "" {
		t.Fatalf("unexpected checkout response: %+v", body)
	}
	if got := db.checkouts[body.ID]; got.UserID != "alice" || got.PlanID != "pro-monthly" {
		t.Fatalf("checkout session not persisted, got %+v", got)
	}
}
