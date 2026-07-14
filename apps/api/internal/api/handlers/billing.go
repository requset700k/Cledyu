package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/store"
)

const (
	checkoutProviderMock    = "mock"
	checkoutProviderToss    = "toss"
	checkoutStatusPending   = "pending"
	checkoutStatusConfirmed = "confirmed"
	checkoutStatusDone      = "completed"
	defaultPlanID           = "free"
)

type billingPlan struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	PriceKRW    int      `json:"price_krw"`
	Interval    string   `json:"interval"`
	Features    []string `json:"features"`
	Recommended bool     `json:"recommended"`
}

type subscriptionResponse struct {
	PlanID           string     `json:"plan_id"`
	Status           string     `json:"status"`
	CurrentPeriodEnd *time.Time `json:"current_period_end,omitempty"`
}

type checkoutRequest struct {
	PlanID string `json:"plan_id" binding:"required"`
}

type checkoutResponse struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Status      string    `json:"status"`
	CheckoutURL string    `json:"checkout_url"`
	ExpiresAt   time.Time `json:"expires_at"`
	ClientKey   string    `json:"client_key,omitempty"`
	CustomerKey string    `json:"customer_key,omitempty"`
	Amount      int       `json:"amount,omitempty"`
	OrderID     string    `json:"order_id,omitempty"`
	OrderName   string    `json:"order_name,omitempty"`
	SuccessURL  string    `json:"success_url,omitempty"`
	FailURL     string    `json:"fail_url,omitempty"`
}

type checkoutRecoverResponse struct {
	PlanID           string     `json:"plan_id"`
	Status           string     `json:"status"`
	CurrentPeriodEnd *time.Time `json:"current_period_end,omitempty"`
}

type tossConfirmPayload struct {
	PaymentKey string
	OrderID    string
	Amount     int
}

type tossConfirmer interface {
	Confirm(context.Context, config.BillingConfig, tossConfirmPayload) error
}

type defaultTossConfirmer struct{}

var billingPlans = []billingPlan{
	{
		ID:       defaultPlanID,
		Name:     "Free",
		PriceKRW: 0,
		Interval: "none",
		Features: []string{
			"공개 Lab 카탈로그",
			"기본 학습 현황",
		},
	},
	{
		ID:          "pro-monthly",
		Name:        "Pro",
		PriceKRW:    9900,
		Interval:    "month",
		Recommended: true,
		Features: []string{
			"전체 Hands-on Lab",
			"AI 힌트 사용량 확장",
			"배지와 수료 이력",
		},
	},
	{
		ID:       "team-monthly",
		Name:     "Team",
		PriceKRW: 29900,
		Interval: "month",
		Features: []string{
			"팀 단위 학습 현황",
			"강사 모드",
			"운영 리포트 준비",
		},
	},
}

// GetBillingPlans는 결제 UI가 표시할 고정 요금제 목록을 반환한다.
// 실제 PG 상품 ID 매핑은 provider 확정 후 별도 테이블/설정으로 분리한다.
func (h *Handler) GetBillingPlans(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"items": billingPlans})
}

// GetMySubscription은 현재 로그인 사용자의 구독 상태를 반환한다.
// 구독 행이 없으면 무료 플랜으로 해석한다.
func (h *Handler) GetMySubscription(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		h.err(c, http.StatusUnauthorized, "missing user")
		return
	}
	if h.db == nil {
		c.JSON(http.StatusOK, subscriptionResponse{PlanID: defaultPlanID, Status: "free"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), dbTimeout)
	defer cancel()
	sub, err := h.db.GetSubscription(ctx, userID)
	if err != nil {
		h.err(c, http.StatusInternalServerError, "subscription lookup failed")
		return
	}
	if sub == nil {
		c.JSON(http.StatusOK, subscriptionResponse{PlanID: defaultPlanID, Status: "free"})
		return
	}
	c.JSON(http.StatusOK, subscriptionResponse{
		PlanID:           sub.PlanID,
		Status:           sub.Status,
		CurrentPeriodEnd: sub.CurrentPeriodEnd,
	})
}

// CreateCheckout는 결제 provider로 넘길 checkout 계약을 만든다.
// mock은 로컬 QA 전용이고, toss는 프론트가 Toss SDK 결제창을 띄울 수 있는 파라미터를 함께 반환한다.
func (h *Handler) CreateCheckout(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "billing store not configured")
		return
	}
	userID := c.GetString("user_id")
	if userID == "" {
		h.err(c, http.StatusUnauthorized, "missing user")
		return
	}

	var req checkoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.err(c, http.StatusBadRequest, err.Error())
		return
	}
	plan, ok := findBillingPlan(req.PlanID)
	if !ok {
		h.err(c, http.StatusBadRequest, "unknown billing plan")
		return
	}
	if plan.PriceKRW == 0 {
		h.err(c, http.StatusBadRequest, "free plan does not require checkout")
		return
	}

	id := newCheckoutID()
	provider := h.billingProvider()
	if provider != checkoutProviderMock && provider != checkoutProviderToss {
		h.err(c, http.StatusServiceUnavailable, "billing provider not supported")
		return
	}
	if provider == checkoutProviderMock && h.cfg != nil && h.cfg.Server.Mode == "release" {
		h.err(c, http.StatusServiceUnavailable, "billing provider not configured")
		return
	}
	if provider == checkoutProviderToss && !h.tossConfigured() {
		h.err(c, http.StatusServiceUnavailable, "toss billing provider not configured")
		return
	}

	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	checkoutURL := "/billing?checkout_session_id=" + id + "&provider=" + provider
	record := store.CheckoutSession{
		ID:          id,
		UserID:      userID,
		PlanID:      plan.ID,
		Provider:    provider,
		Status:      checkoutStatusPending,
		CheckoutURL: checkoutURL,
		ExpiresAt:   expiresAt,
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), dbTimeout)
	defer cancel()
	if err := h.db.CreateCheckoutSession(ctx, record); err != nil {
		h.err(c, http.StatusInternalServerError, "checkout session create failed")
		return
	}

	resp := checkoutResponse{
		ID:          id,
		Provider:    provider,
		Status:      checkoutStatusPending,
		CheckoutURL: checkoutURL,
		ExpiresAt:   expiresAt,
	}
	if provider == checkoutProviderToss {
		resp.ClientKey = h.cfg.Billing.TossClientKey
		resp.CustomerKey = customerKeyFromUserID(userID)
		resp.Amount = plan.PriceKRW
		resp.OrderID = id
		resp.OrderName = plan.Name + " 월간 구독"
		resp.SuccessURL = h.apiURL("/api/v1/billing/toss/success")
		resp.FailURL = h.frontendURL("/billing?checkout_result=failed")
	}

	c.JSON(http.StatusCreated, resp)
}

// CompleteCheckout는 mock provider 승인 완료를 시뮬레이션한다.
// 실제 PG 웹훅 전까지 프론트가 결제 완료 후 구독 상태 변화를 검증할 수 있게 한다.
func (h *Handler) CompleteCheckout(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "billing store not configured")
		return
	}
	userID := c.GetString("user_id")
	if userID == "" {
		h.err(c, http.StatusUnauthorized, "missing user")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), dbTimeout)
	defer cancel()
	sub, err := h.db.CompleteCheckoutSession(ctx, c.Param("id"), userID)
	if errors.Is(err, store.ErrCheckoutNotFound) {
		h.err(c, http.StatusNotFound, "checkout session not found")
		return
	}
	if errors.Is(err, store.ErrCheckoutExpired) {
		h.err(c, http.StatusConflict, "checkout session expired")
		return
	}
	if errors.Is(err, store.ErrCheckoutInvalidStatus) {
		h.err(c, http.StatusConflict, "checkout session cannot be completed")
		return
	}
	if err != nil {
		h.err(c, http.StatusInternalServerError, "checkout completion failed")
		return
	}

	c.JSON(http.StatusOK, subscriptionResponse{
		PlanID:           sub.PlanID,
		Status:           sub.Status,
		CurrentPeriodEnd: sub.CurrentPeriodEnd,
	})
}

// RecoverCheckout은 Toss confirm 이후 confirmed 상태로 남은 checkout을 사용자가 다시 완료하게 한다.
// 실제 결제 승인은 이미 ConfirmTossCheckout에서 끝났으므로 여기서는 소유자와 상태만 확인하고 구독 upsert를 재시도한다.
func (h *Handler) RecoverCheckout(c *gin.Context) {
	if h.db == nil {
		h.err(c, http.StatusServiceUnavailable, "billing store not configured")
		return
	}
	userID := c.GetString("user_id")
	if userID == "" {
		h.err(c, http.StatusUnauthorized, "missing user")
		return
	}

	checkoutID := c.Param("id")
	ctx, cancel := context.WithTimeout(c.Request.Context(), dbTimeout)
	cs, err := h.db.GetCheckoutSession(ctx, checkoutID, userID)
	cancel()
	if errors.Is(err, store.ErrCheckoutNotFound) {
		h.err(c, http.StatusNotFound, "checkout session not found")
		return
	}
	if err != nil {
		h.err(c, http.StatusInternalServerError, "checkout session lookup failed")
		return
	}
	if cs.Provider != checkoutProviderToss || cs.Status != checkoutStatusConfirmed {
		h.err(c, http.StatusConflict, "checkout session cannot be recovered")
		return
	}

	completeCtx, cancel := context.WithTimeout(c.Request.Context(), dbTimeout)
	sub, err := h.db.CompleteCheckoutSession(completeCtx, checkoutID, userID)
	cancel()
	if errors.Is(err, store.ErrCheckoutInvalidStatus) || errors.Is(err, store.ErrCheckoutNotFound) {
		h.err(c, http.StatusConflict, "checkout session cannot be recovered")
		return
	}
	if err != nil || sub == nil {
		h.err(c, http.StatusInternalServerError, "checkout recovery failed")
		return
	}
	c.JSON(http.StatusOK, checkoutRecoverResponse{
		PlanID:           sub.PlanID,
		Status:           sub.Status,
		CurrentPeriodEnd: sub.CurrentPeriodEnd,
	})
}

// ConfirmTossCheckout은 Toss success redirect를 받아 서버에서 최종 승인(confirm)을 수행한다.
// 브라우저가 전달한 amount/orderId는 DB에 저장된 checkout 세션과 다시 대조한다.
func (h *Handler) ConfirmTossCheckout(c *gin.Context) {
	if h.db == nil {
		h.redirectBilling(c, "failed", "billing store not configured")
		return
	}
	if h.billingProvider() != checkoutProviderToss || !h.tossConfigured() {
		h.redirectBilling(c, "failed", "toss billing provider not configured")
		return
	}
	paymentKey := strings.TrimSpace(c.Query("paymentKey"))
	orderID := strings.TrimSpace(c.Query("orderId"))
	amount, err := strconv.Atoi(strings.TrimSpace(c.Query("amount")))
	if paymentKey == "" || orderID == "" || err != nil {
		h.redirectBilling(c, "failed", "invalid toss callback")
		return
	}

	lookupCtx, cancel := context.WithTimeout(c.Request.Context(), dbTimeout)
	cs, err := h.db.GetCheckoutSessionByID(lookupCtx, orderID)
	cancel()
	if errors.Is(err, store.ErrCheckoutNotFound) {
		h.redirectBilling(c, "failed", "checkout session not found")
		return
	}
	if err != nil {
		h.redirectBilling(c, "failed", "checkout session lookup failed")
		return
	}
	plan, ok := findBillingPlan(cs.PlanID)
	if !ok || cs.Provider != checkoutProviderToss || amount != plan.PriceKRW {
		h.redirectBilling(c, "failed", "checkout verification failed")
		return
	}
	if cs.Status != checkoutStatusPending && cs.Status != checkoutStatusConfirmed && cs.Status != checkoutStatusDone {
		h.redirectBilling(c, "failed", "checkout session cannot be completed")
		return
	}
	if cs.Status == checkoutStatusPending && cs.ExpiresAt.Before(time.Now().UTC()) {
		h.redirectBilling(c, "failed", "checkout session expired")
		return
	}
	if cs.Status == checkoutStatusPending {
		confirmer := h.toss
		if confirmer == nil {
			confirmer = defaultTossConfirmer{}
		}
		confirmCtx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		err := confirmer.Confirm(confirmCtx, h.cfg.Billing, tossConfirmPayload{
			PaymentKey: paymentKey,
			OrderID:    orderID,
			Amount:     amount,
		})
		cancel()
		if err != nil {
			h.redirectBilling(c, "failed", "toss payment confirm failed")
			return
		}
		recordCtx, cancel := context.WithTimeout(c.Request.Context(), dbTimeout)
		err = h.db.MarkCheckoutConfirmed(recordCtx, orderID)
		cancel()
		if err != nil {
			h.redirectBilling(c, "failed", "checkout confirmation record failed")
			return
		}
	}

	completeCtx, cancel := context.WithTimeout(c.Request.Context(), dbTimeout)
	sub, err := h.db.CompleteCheckoutSession(completeCtx, orderID, cs.UserID)
	cancel()
	if errors.Is(err, store.ErrCheckoutExpired) {
		h.redirectBilling(c, "failed", "checkout session expired")
		return
	}
	if errors.Is(err, store.ErrCheckoutInvalidStatus) || errors.Is(err, store.ErrCheckoutNotFound) {
		h.redirectBilling(c, "failed", "checkout session cannot be completed")
		return
	}
	if err != nil || sub == nil {
		h.redirectBillingWithCheckout(c, "failed", "checkout completion failed", orderID, checkoutProviderToss)
		return
	}
	c.Redirect(http.StatusFound, h.frontendURL("/billing?checkout_result=success&checkout_session_id="+url.QueryEscape(orderID)+"&provider=toss"))
}

func findBillingPlan(planID string) (billingPlan, bool) {
	for _, plan := range billingPlans {
		if plan.ID == planID {
			return plan, true
		}
	}
	return billingPlan{}, false
}

func newCheckoutID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "chk_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return "chk_" + hex.EncodeToString(b)
}

func (h *Handler) billingProvider() string {
	if h == nil || h.cfg == nil {
		return checkoutProviderMock
	}
	provider := strings.ToLower(strings.TrimSpace(h.cfg.Billing.Provider))
	if provider == "" {
		return checkoutProviderMock
	}
	return provider
}

func (h *Handler) tossConfigured() bool {
	if h == nil || h.cfg == nil {
		return false
	}
	return strings.TrimSpace(h.cfg.Billing.TossClientKey) != "" &&
		strings.TrimSpace(h.cfg.Billing.TossSecretKey) != "" &&
		strings.TrimSpace(h.cfg.Billing.TossAPIBaseURL) != ""
}

func (h *Handler) apiURL(path string) string {
	if h != nil && h.cfg != nil {
		if u, err := url.Parse(h.cfg.Keycloak.RedirectURI); err == nil && u.Scheme != "" && u.Host != "" {
			u.Path = path
			u.RawQuery = ""
			u.Fragment = ""
			return u.String()
		}
	}
	return path
}

func (h *Handler) frontendURL(path string) string {
	base := "https://app.cledyu.local"
	if h != nil && h.cfg != nil && strings.TrimSpace(h.cfg.FrontendURL) != "" {
		base = strings.TrimRight(strings.TrimSpace(h.cfg.FrontendURL), "/")
	}
	if strings.HasPrefix(path, "/") {
		return base + path
	}
	return base + "/" + path
}

func (h *Handler) redirectBilling(c *gin.Context, result, message string) {
	h.redirectBillingWithCheckout(c, result, message, "", "")
}

func (h *Handler) redirectBillingWithCheckout(c *gin.Context, result, message, checkoutID, provider string) {
	q := url.Values{}
	q.Set("checkout_result", result)
	if message != "" {
		q.Set("message", message)
	}
	if checkoutID != "" {
		q.Set("checkout_session_id", checkoutID)
	}
	if provider != "" {
		q.Set("provider", provider)
	}
	c.Redirect(http.StatusFound, h.frontendURL("/billing?"+q.Encode()))
}

func customerKeyFromUserID(userID string) string {
	sum := sha256.Sum256([]byte(userID))
	return "cledyu_" + hex.EncodeToString(sum[:])[:32]
}

func (defaultTossConfirmer) Confirm(ctx context.Context, cfg config.BillingConfig, payload tossConfirmPayload) error {
	body, err := json.Marshal(gin.H{
		"paymentKey": payload.PaymentKey,
		"orderId":    payload.OrderID,
		"amount":     payload.Amount,
	})
	if err != nil {
		return fmt.Errorf("marshal toss confirm body: %w", err)
	}
	endpoint := strings.TrimRight(cfg.TossAPIBaseURL, "/") + "/v1/payments/confirm"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new toss confirm request: %w", err)
	}
	token := base64.StdEncoding.EncodeToString([]byte(cfg.TossSecretKey + ":"))
	req.Header.Set("Authorization", "Basic "+token)
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("call toss confirm: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("toss confirm failed: status=%d body=%s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
