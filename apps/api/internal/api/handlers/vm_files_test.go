package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/session"
	"github.com/requset700k/cledyu/api/internal/vmfiles"
	"go.uber.org/zap"
)

type vmFileSessionProvider struct {
	sessions map[string]*session.Session
	err      error
}

func (p *vmFileSessionProvider) Create(context.Context, string, string, string, session.BootInit) (*session.Session, error) {
	return nil, errors.New("not implemented")
}

func (p *vmFileSessionProvider) Get(_ context.Context, sessionID string) (*session.Session, error) {
	if p.err != nil {
		return nil, p.err
	}
	if sess, ok := p.sessions[sessionID]; ok {
		return sess, nil
	}
	return nil, session.ErrNotFound
}

func (p *vmFileSessionProvider) Delete(context.Context, string) error { return nil }
func (p *vmFileSessionProvider) FindActiveByUser(context.Context, string) (string, error) {
	return "", nil
}
func (p *vmFileSessionProvider) CountActiveSessions(context.Context) (int, error) { return 0, nil }
func (p *vmFileSessionProvider) ReapStuckSessions(context.Context, time.Duration) ([]string, error) {
	return nil, nil
}
func (p *vmFileSessionProvider) ReapExpiredSessions(context.Context) ([]string, error) {
	return nil, nil
}
func (p *vmFileSessionProvider) VMIAddress(context.Context, string) (string, error) {
	return "", nil
}
func (p *vmFileSessionProvider) Capacity() int { return 0 }

type vmFileServiceStub struct {
	snapshot vmfiles.Snapshot
	err      error

	listCalls int
}

func (s *vmFileServiceStub) List(context.Context, string) (vmfiles.Snapshot, error) {
	s.listCalls++
	return s.snapshot, s.err
}

func (s *vmFileServiceStub) Read(context.Context, string, string) ([]byte, error) { return nil, nil }

func vmFileRouter(h *Handler, uid string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	identify := func(c *gin.Context) {
		if uid != "" {
			c.Set("user_id", uid)
		}
		c.Next()
	}
	r.GET("/sessions/:id/files", identify, h.ListSessionFiles)
	return r
}

func vmFileHandler(status string, service *vmFileServiceStub) *Handler {
	return &Handler{
		log: zap.NewNop(),
		sessions: &vmFileSessionProvider{sessions: map[string]*session.Session{
			"s1": {
				ID:        "s1",
				LabID:     "lab-linux-basics",
				UserID:    "alice",
				Status:    status,
				StartedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
				Provider:  session.ProviderKubeVirt,
			},
		}},
		vmFiles: service,
	}
}

func TestListSessionFilesAllowsOwner(t *testing.T) {
	service := &vmFileServiceStub{snapshot: vmfiles.Snapshot{
		Root: "/home/lab",
		Items: []vmfiles.Entry{
			{Path: "work", Name: "work", Type: "directory", Depth: 1},
			{Path: "work/app.log", Name: "app.log", Type: "file", Depth: 2},
		},
	}}
	h := vmFileHandler("ready", service)
	r := vmFileRouter(h, "alice")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sessions/s1/files", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got vmfiles.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Root != "/home/lab" || len(got.Items) != 2 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if service.listCalls != 1 {
		t.Fatalf("List calls = %d, want 1", service.listCalls)
	}
}

func TestListSessionFilesHidesOtherUsersSession(t *testing.T) {
	service := &vmFileServiceStub{}
	h := vmFileHandler("ready", service)
	r := vmFileRouter(h, "bob")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sessions/s1/files", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if service.listCalls != 0 {
		t.Fatalf("List calls = %d, want 0 for non-owner", service.listCalls)
	}
}

func TestListSessionFilesRejectsProvisioningSession(t *testing.T) {
	service := &vmFileServiceStub{}
	h := vmFileHandler("provisioning", service)
	r := vmFileRouter(h, "alice")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sessions/s1/files", nil))

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if service.listCalls != 0 {
		t.Fatalf("List calls = %d, want 0 before ready", service.listCalls)
	}
}

func TestListSessionFilesMapsBusyToTooManyRequests(t *testing.T) {
	service := &vmFileServiceStub{err: vmfiles.ErrBusy}
	h := vmFileHandler("ready", service)
	r := vmFileRouter(h, "alice")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sessions/s1/files", nil))

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", w.Code, w.Body.String())
	}
}
