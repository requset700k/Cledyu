package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/lock"
	"github.com/requset700k/cledyu/api/internal/session"
	"github.com/requset700k/cledyu/api/internal/store"
	"go.uber.org/zap"
)

type entitlementSessionProvider struct {
	createCount int
}

func (p *entitlementSessionProvider) Create(_ context.Context, sessionID, labID, userID string, _ session.BootInit) (*session.Session, error) {
	p.createCount++
	return &session.Session{
		ID:        sessionID,
		LabID:     labID,
		UserID:    userID,
		Status:    "provisioning",
		StartedAt: time.Now(),
		ExpiresAt: time.Now().Add(3 * time.Hour),
		Provider:  session.ProviderKubeVirt,
	}, nil
}

func (p *entitlementSessionProvider) Get(context.Context, string) (*session.Session, error) {
	return nil, session.ErrNotFound
}
func (p *entitlementSessionProvider) Delete(context.Context, string) error { return nil }
func (p *entitlementSessionProvider) FindActiveByUser(context.Context, string) (string, error) {
	return "", nil
}
func (p *entitlementSessionProvider) CountActiveSessions(context.Context) (int, error) { return 0, nil }
func (p *entitlementSessionProvider) ReapStuckSessions(context.Context, time.Duration) ([]string, error) {
	return nil, nil
}
func (p *entitlementSessionProvider) ReapExpiredSessions(context.Context) ([]string, error) {
	return nil, nil
}
func (p *entitlementSessionProvider) VMIAddress(context.Context, string) (string, error) {
	return "", session.ErrNotFound
}
func (p *entitlementSessionProvider) Capacity() int { return 0 }

func newEntitlementRouter(t *testing.T, mode string, db *fakePersistence, provider *entitlementSessionProvider) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	labs, err := content.Load()
	if err != nil {
		t.Fatalf("load labs: %v", err)
	}
	h := &Handler{
		cfg:      &config.Config{Server: config.ServerConfig{Mode: mode}},
		log:      zap.NewNop(),
		labs:     labs,
		sessions: provider,
		steps:    newStepStore(db, zap.NewNop()),
		db:       db,
		locks:    lock.NewMemLocker(),
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", "u1")
		c.Next()
	})
	r.POST("/sessions", h.CreateSession)
	return r
}

func postSession(t *testing.T, r *gin.Engine, labID string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]string{"lab_id": labID}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sessions", &buf)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func TestCreateSessionRequiresSubscriptionForPaidLabs(t *testing.T) {
	db := newFakePersistence()
	provider := &entitlementSessionProvider{}
	r := newEntitlementRouter(t, "debug", db, provider)

	w, body := postSession(t, r, "lab-k8s-basics")
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status=%d body=%v, want 402", w.Code, body)
	}
	if body["code"] != "subscription_required" || body["required_plan"] != requiredPaidPlanID {
		t.Fatalf("unexpected entitlement error payload: %v", body)
	}
	if provider.createCount != 0 {
		t.Fatalf("paid lab without subscription must not create VM session, createCount=%d", provider.createCount)
	}
}

func TestCreateSessionAllowsBeginnerLabsWithoutSubscription(t *testing.T) {
	db := newFakePersistence()
	provider := &entitlementSessionProvider{}
	r := newEntitlementRouter(t, "debug", db, provider)

	w, body := postSession(t, r, "lab-docker-basics")
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%v, want 201", w.Code, body)
	}
	if provider.createCount != 1 {
		t.Fatalf("beginner lab should create VM session, createCount=%d", provider.createCount)
	}
}

func TestCreateSessionAllowsPaidLabsForActiveSubscription(t *testing.T) {
	db := newFakePersistence()
	periodEnd := time.Now().Add(24 * time.Hour)
	db.subscriptions["u1"] = store.Subscription{
		UserID:           "u1",
		PlanID:           "pro-monthly",
		Status:           "active",
		CurrentPeriodEnd: &periodEnd,
	}
	provider := &entitlementSessionProvider{}
	r := newEntitlementRouter(t, "debug", db, provider)

	w, body := postSession(t, r, "lab-k8s-basics")
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%v, want 201", w.Code, body)
	}
	if provider.createCount != 1 {
		t.Fatalf("active paid subscription should create VM session, createCount=%d", provider.createCount)
	}
}

func TestCreateSessionDoesNotGatePaidLabsInReleaseBeforeProviderCallback(t *testing.T) {
	db := newFakePersistence()
	provider := &entitlementSessionProvider{}
	r := newEntitlementRouter(t, "release", db, provider)

	w, body := postSession(t, r, "lab-k8s-basics")
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%v, want 201", w.Code, body)
	}
	if provider.createCount != 1 {
		t.Fatalf("release should not block paid labs before provider callback exists, createCount=%d", provider.createCount)
	}
}
