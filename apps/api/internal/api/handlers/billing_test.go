package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/store"
	"go.uber.org/zap"
)

type fakeTossConfirmer struct {
	calls   int
	payload tossConfirmPayload
}

func (f *fakeTossConfirmer) Confirm(_ context.Context, _ config.BillingConfig, payload tossConfirmPayload) error {
	f.calls++
	f.payload = payload
	return nil
}

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
	periodEnd := time.Now().Add(7 * 24 * time.Hour)
	fake.checkouts["chk_done"] = store.CheckoutSession{
		ID:        "chk_done",
		UserID:    "u1",
		PlanID:    "team-monthly",
		Provider:  checkoutProviderMock,
		Status:    "completed",
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	fake.subscriptions["u1"] = store.Subscription{
		UserID:           "u1",
		PlanID:           "team-monthly",
		Status:           "active",
		CurrentPeriodEnd: &periodEnd,
		UpdatedAt:        time.Now().Add(-time.Hour),
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
	if got := fake.subscriptions["u1"].CurrentPeriodEnd; got == nil || !got.Equal(periodEnd) {
		t.Fatalf("completed checkout must not extend period_end, got %v want %v", got, periodEnd)
	}
}

func TestConfirmTossCheckout_ConfirmsPaymentAndActivatesSubscription(t *testing.T) {
	fake := newFakePersistence()
	fake.checkouts["chk_toss"] = store.CheckoutSession{
		ID:        "chk_toss",
		UserID:    "u1",
		PlanID:    "pro-monthly",
		AmountKRW: 9900,
		Provider:  checkoutProviderToss,
		Status:    checkoutStatusPending,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	confirmer := &fakeTossConfirmer{}
	h := &Handler{
		cfg: &config.Config{
			FrontendURL: "https://app.cledyu.local",
			Keycloak:    config.KeycloakConfig{RedirectURI: "https://api.cledyu.local/api/v1/auth/callback"},
			Billing: config.BillingConfig{
				Provider:       checkoutProviderToss,
				TossClientKey:  "test_ck",
				TossSecretKey:  "test_sk",
				TossAPIBaseURL: "https://api.tosspayments.com",
			},
		},
		log:  zap.NewNop(),
		db:   fake,
		toss: confirmer,
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/billing/toss/success", h.ConfirmTossCheckout)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/billing/toss/success?paymentKey=pay_1&orderId=chk_toss&amount=9900", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%s", w.Code, w.Body.String())
	}
	if confirmer.calls != 1 {
		t.Fatalf("expected one toss confirm call, got %d", confirmer.calls)
	}
	if confirmer.payload.OrderID != "chk_toss" || confirmer.payload.Amount != 9900 {
		t.Fatalf("unexpected confirm payload: %+v", confirmer.payload)
	}
	if got := fake.checkouts["chk_toss"].Status; got != "completed" {
		t.Fatalf("checkout status = %q, want completed", got)
	}
	sub := fake.subscriptions["u1"]
	if sub.PlanID != "pro-monthly" || sub.Status != "active" {
		t.Fatalf("subscription mismatch: %+v", sub)
	}
}

func TestConfirmTossCheckout_ConfirmedSessionCompletesWithoutReconfirm(t *testing.T) {
	fake := newFakePersistence()
	fake.checkouts["chk_confirmed"] = store.CheckoutSession{
		ID:        "chk_confirmed",
		UserID:    "u1",
		PlanID:    "pro-monthly",
		AmountKRW: 9900,
		Provider:  checkoutProviderToss,
		Status:    checkoutStatusConfirmed,
		ExpiresAt: time.Now().Add(-10 * time.Minute),
	}
	confirmer := &fakeTossConfirmer{}
	h := &Handler{
		cfg: &config.Config{
			FrontendURL: "https://app.cledyu.local",
			Billing: config.BillingConfig{
				Provider:       checkoutProviderToss,
				TossClientKey:  "test_ck",
				TossSecretKey:  "test_sk",
				TossAPIBaseURL: "https://api.tosspayments.com",
			},
		},
		log:  zap.NewNop(),
		db:   fake,
		toss: confirmer,
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/billing/toss/success", h.ConfirmTossCheckout)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/billing/toss/success?paymentKey=pay_1&orderId=chk_confirmed&amount=9900", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%s", w.Code, w.Body.String())
	}
	if confirmer.calls != 0 {
		t.Fatalf("confirmed checkout must not call toss confirm again, got %d calls", confirmer.calls)
	}
	if got := fake.checkouts["chk_confirmed"].Status; got != checkoutStatusDone {
		t.Fatalf("checkout status = %q, want completed", got)
	}
	sub := fake.subscriptions["u1"]
	if sub.PlanID != "pro-monthly" || sub.Status != "active" {
		t.Fatalf("subscription mismatch: %+v", sub)
	}
}

func TestRecoverCheckout_CompletesConfirmedTossSession(t *testing.T) {
	fake := newFakePersistence()
	fake.checkouts["chk_confirmed"] = store.CheckoutSession{
		ID:        "chk_confirmed",
		UserID:    "u1",
		PlanID:    "pro-monthly",
		AmountKRW: 9900,
		Provider:  checkoutProviderToss,
		Status:    checkoutStatusConfirmed,
		ExpiresAt: time.Now().Add(-10 * time.Minute),
	}
	h := &Handler{log: zap.NewNop(), db: fake}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/billing/checkout/:id/recover", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.RecoverCheckout(c)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/billing/checkout/chk_confirmed/recover", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got := fake.checkouts["chk_confirmed"].Status; got != checkoutStatusDone {
		t.Fatalf("checkout status = %q, want completed", got)
	}
	sub := fake.subscriptions["u1"]
	if sub.PlanID != "pro-monthly" || sub.Status != "active" {
		t.Fatalf("subscription mismatch: %+v", sub)
	}
}

func TestConfirmTossCheckout_RejectsAmountMismatchBeforeConfirm(t *testing.T) {
	fake := newFakePersistence()
	fake.checkouts["chk_toss"] = store.CheckoutSession{
		ID:        "chk_toss",
		UserID:    "u1",
		PlanID:    "pro-monthly",
		AmountKRW: 9900,
		Provider:  checkoutProviderToss,
		Status:    checkoutStatusPending,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	confirmer := &fakeTossConfirmer{}
	h := &Handler{
		cfg: &config.Config{
			FrontendURL: "https://app.cledyu.local",
			Billing: config.BillingConfig{
				Provider:       checkoutProviderToss,
				TossClientKey:  "test_ck",
				TossSecretKey:  "test_sk",
				TossAPIBaseURL: "https://api.tosspayments.com",
			},
		},
		log:  zap.NewNop(),
		db:   fake,
		toss: confirmer,
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/billing/toss/success", h.ConfirmTossCheckout)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/billing/toss/success?paymentKey=pay_1&orderId=chk_toss&amount=100", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%s", w.Code, w.Body.String())
	}
	if confirmer.calls != 0 {
		t.Fatalf("amount mismatch must not call toss confirm, got %d calls", confirmer.calls)
	}
	if got := fake.checkouts["chk_toss"].Status; got != "pending" {
		t.Fatalf("checkout status = %q, want pending", got)
	}
}

func TestConfirmTossCheckout_UsesCheckoutAmountSnapshot(t *testing.T) {
	fake := newFakePersistence()
	fake.checkouts["chk_toss_snapshot"] = store.CheckoutSession{
		ID:        "chk_toss_snapshot",
		UserID:    "u1",
		PlanID:    "pro-monthly",
		AmountKRW: 8800,
		Provider:  checkoutProviderToss,
		Status:    checkoutStatusPending,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	confirmer := &fakeTossConfirmer{}
	h := &Handler{
		cfg: &config.Config{
			FrontendURL: "https://app.cledyu.local",
			Billing: config.BillingConfig{
				Provider:       checkoutProviderToss,
				TossClientKey:  "test_ck",
				TossSecretKey:  "test_sk",
				TossAPIBaseURL: "https://api.tosspayments.com",
			},
		},
		log:  zap.NewNop(),
		db:   fake,
		toss: confirmer,
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/billing/toss/success", h.ConfirmTossCheckout)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/billing/toss/success?paymentKey=pay_1&orderId=chk_toss_snapshot&amount=8800", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%s", w.Code, w.Body.String())
	}
	if confirmer.payload.Amount != 8800 {
		t.Fatalf("confirm payload amount = %d, want checkout snapshot 8800", confirmer.payload.Amount)
	}
	if got := fake.checkouts["chk_toss_snapshot"].Status; got != checkoutStatusDone {
		t.Fatalf("checkout status = %q, want completed", got)
	}
}

func TestCreateCheckout_BlocksMockProviderInRelease(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			Server:  config.ServerConfig{Mode: "release"},
			Billing: config.BillingConfig{Provider: checkoutProviderMock},
		},
		log: zap.NewNop(),
		db:  newFakePersistence(),
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/billing/checkout", func(c *gin.Context) {
		c.Set("user_id", "u1")
		h.CreateCheckout(c)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/billing/checkout", strings.NewReader(`{"plan_id":"pro-monthly"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}
