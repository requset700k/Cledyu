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
	errSubscriptionRequired        = errors.New("active subscription required")
	errEntitlementStoreUnavailable = errors.New("entitlement store not configured")
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
	if !labRequiresPaidPlan(lab) {
		return nil
	}

	if h.db == nil {
		if h.cfg != nil && h.cfg.Server.Mode == "release" {
			return errEntitlementStoreUnavailable
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
