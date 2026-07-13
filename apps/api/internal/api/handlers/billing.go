package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/store"
)

const (
	checkoutProviderMock  = "mock"
	checkoutStatusPending = "pending"
	defaultPlanID         = "free"
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
}

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

// CreateCheckout는 결제 provider 연동 전 단계의 checkout 계약을 만든다.
// 현재는 실제 과금 없이 pending mock 세션만 저장한다.
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
	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	checkoutURL := "/billing?checkout_session_id=" + id + "&provider=" + checkoutProviderMock
	record := store.CheckoutSession{
		ID:          id,
		UserID:      userID,
		PlanID:      plan.ID,
		Provider:    checkoutProviderMock,
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

	c.JSON(http.StatusCreated, checkoutResponse{
		ID:          id,
		Provider:    checkoutProviderMock,
		Status:      checkoutStatusPending,
		CheckoutURL: checkoutURL,
		ExpiresAt:   expiresAt,
	})
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
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "chk_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return "chk_" + hex.EncodeToString(b)
}
