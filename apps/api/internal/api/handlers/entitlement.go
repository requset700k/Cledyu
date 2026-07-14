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
	if h.cfg == nil {
		return nil
	}
	if !labRequiresPaidPlan(lab) {
		return nil
	}

	enforcePaidPlan := true
	// 실제 결제 provider가 없는 release 배포에서는 운영 데모를 막지 않기 위해 임시 우회한다.
	// Toss provider가 설정된 release부터는 결제 활성화 경로가 있으므로 유료 Lab 구독 검사를 적용한다.
	if h.cfg.Server.Mode == "release" && h.billingProvider() != checkoutProviderToss {
		enforcePaidPlan = false
	}
	if !enforcePaidPlan {
		return nil
	}

	if h.db == nil {
		if enforcePaidPlan && h.cfg.Server.Mode == "release" && h.billingProvider() == checkoutProviderToss {
			return errSubscriptionRequired
		}
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
