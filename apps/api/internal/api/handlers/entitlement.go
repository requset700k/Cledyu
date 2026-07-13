package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/store"
)

const requiredPaidPlanID = "pro-monthly"

var (
	errSubscriptionRequired = errors.New("active subscription required")
)

func labRequiresPaidPlan(lab content.LabContent) bool {
	// Lab DSL에 access/plan 필드가 생기기 전까지는 난이도로 결제 게이트를 둔다.
	// beginner는 무료 체험, intermediate/advanced는 유료 구독 대상으로 본다.
	return lab.Difficulty != "beginner"
}

func hasActivePaidSubscription(sub *store.Subscription, now time.Time) bool {
	if sub == nil || sub.Status != "active" || sub.PlanID == defaultPlanID {
		return false
	}
	if sub.CurrentPeriodEnd != nil && !sub.CurrentPeriodEnd.After(now) {
		return false
	}
	return true
}

func (h *Handler) ensureLabEntitlement(ctx context.Context, userID string, lab content.LabContent) error {
	// Release에는 아직 실제 결제 provider callback/webhook이 없으므로 유료 Lab을 차단하지 않는다.
	// Mock checkout 완료 라우트가 등록되는 non-release 환경에서만 1차 entitlement 계약을 검증한다.
	if h.cfg == nil || h.cfg.Server.Mode == "release" {
		return nil
	}

	if !labRequiresPaidPlan(lab) {
		return nil
	}

	if h.db == nil {
		return nil
	}

	sub, err := h.db.GetSubscription(ctx, userID)
	if err != nil {
		return err
	}
	if !hasActivePaidSubscription(sub, time.Now().UTC()) {
		return errSubscriptionRequired
	}
	return nil
}
