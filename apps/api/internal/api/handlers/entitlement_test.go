package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	createCount   int
	createErr     error
	activeID      string
	activeSession *session.Session
	deletedID     string
}

func (p *entitlementSessionProvider) Create(_ context.Context, sessionID, labID, userID string, _ session.BootInit) (*session.Session, error) {
	p.createCount++
	if p.createErr != nil {
		return nil, p.createErr
	}
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

func (p *entitlementSessionProvider) Get(_ context.Context, sessionID string) (*session.Session, error) {
	if p.activeSession != nil && p.activeID == sessionID {
		return p.activeSession, nil
	}
	return nil, session.ErrNotFound
}
func (p *entitlementSessionProvider) Delete(_ context.Context, sessionID string) error {
	p.deletedID = sessionID
	if p.activeID == sessionID {
		p.activeID = ""
		p.activeSession = nil
	}
	return nil
}
func (p *entitlementSessionProvider) FindActiveByUser(context.Context, string) (string, error) {
	return p.activeID, nil
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

func TestCreateSessionDoesNotProvisionWhenInitialProgressSaveFails(t *testing.T) {
	db := newFakePersistence()
	db.saveErr = errors.New("database unavailable")
	provider := &entitlementSessionProvider{}
	r := newEntitlementRouter(t, "debug", db, provider)

	w, body := postSession(t, r, "lab-docker-basics")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%v, want 500", w.Code, body)
	}
	if provider.createCount != 0 {
		t.Fatalf("provider create count=%d, want 0", provider.createCount)
	}
}

func TestCreateSessionRemovesInitialProgressWhenProviderCreateFails(t *testing.T) {
	db := newFakePersistence()
	provider := &entitlementSessionProvider{createErr: errors.New("provider unavailable")}
	r := newEntitlementRouter(t, "debug", db, provider)

	w, body := postSession(t, r, "lab-docker-basics")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%v, want 500", w.Code, body)
	}
	if len(db.progress) != 0 {
		t.Fatalf("progress rows=%d, want 0", len(db.progress))
	}
}

func TestCreateSessionReplacesCompletedActiveSession(t *testing.T) {
	db := newFakePersistence()
	completed := twoStepSeed()
	completed.LabID = "lab-docker-basics"
	completed.UserID = "u1"
	for i := range completed.Steps {
		completed.Steps[i].Status = "passed"
	}
	db.progress["completed-session"] = *toStoreProgress(completed)
	provider := &entitlementSessionProvider{
		activeID: "completed-session",
		activeSession: &session.Session{
			ID:        "completed-session",
			LabID:     "lab-docker-basics",
			UserID:    "u1",
			Status:    "ready",
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	r := newEntitlementRouter(t, "debug", db, provider)

	w, body := postSession(t, r, "lab-docker-basics")
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%v, want 201", w.Code, body)
	}
	if provider.deletedID != "completed-session" {
		t.Fatalf("deleted session=%q, want completed-session", provider.deletedID)
	}
	if provider.createCount != 1 {
		t.Fatalf("provider create count=%d, want 1", provider.createCount)
	}
}

func TestCreateSessionKeepsIncompleteActiveSession(t *testing.T) {
	db := newFakePersistence()
	incomplete := twoStepSeed()
	incomplete.LabID = "lab-docker-basics"
	incomplete.UserID = "u1"
	db.progress["active-session"] = *toStoreProgress(incomplete)
	provider := &entitlementSessionProvider{
		activeID: "active-session",
		activeSession: &session.Session{
			ID:        "active-session",
			LabID:     "lab-docker-basics",
			UserID:    "u1",
			Status:    "ready",
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	r := newEntitlementRouter(t, "debug", db, provider)

	w, body := postSession(t, r, "lab-docker-basics")
	if w.Code != http.StatusConflict || body["code"] != "session_exists" {
		t.Fatalf("status=%d body=%v, want session_exists", w.Code, body)
	}
	if provider.deletedID != "" || provider.createCount != 0 {
		t.Fatalf("incomplete session changed: deleted=%q creates=%d", provider.deletedID, provider.createCount)
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
