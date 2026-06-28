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
	preview  []byte
	err      error
	readErr  error

	listCalls int
	readCalls int
	readPath  string
}

func (s *vmFileServiceStub) List(context.Context, string) (vmfiles.Snapshot, error) {
	s.listCalls++
	return s.snapshot, s.err
}

func (s *vmFileServiceStub) Read(_ context.Context, _ string, relativePath string) ([]byte, error) {
	s.readCalls++
	s.readPath = relativePath
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s.preview, nil
}

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
	r.GET("/sessions/:id/files/preview", identify, h.PreviewSessionFile)
	return r
}

func vmFileHandler(status string, service *vmFileServiceStub) *Handler {
	return vmFileHandlerWithProvider(status, session.ProviderKubeVirt, service)
}

func vmFileHandlerWithProvider(status string, provider string, service *vmFileServiceStub) *Handler {
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
				Provider:  provider,
			},
		}},
		vmFiles: service,
	}
}

func TestListSessionFilesAllowsOwner(t *testing.T) {
	// 정상 경로: 세션 소유자이면서 KubeVirt ready 세션이면 vmfiles.Service.List 결과를 그대로 반환한다.
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
	// 교차 테넌트 방어: 타인 세션은 존재 여부를 노출하지 않도록 404로 숨기고 VM 조회를 시작하지 않는다.
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
	// guest 준비 전 방어: provisioning 세션은 forced command/SSH key가 준비되지 않았을 수 있으므로
	// KubeVirt port-forward까지 내려가지 않는다.
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

func TestListSessionFilesRejectsEC2Session(t *testing.T) {
	// provider 경계: 현재 runner는 KubeVirt session-vm 전용이므로 EC2 overflow 세션은 조회 전에 차단한다.
	service := &vmFileServiceStub{}
	h := vmFileHandlerWithProvider("ready", session.ProviderEC2, service)
	r := vmFileRouter(h, "alice")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sessions/s1/files", nil))

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if service.listCalls != 0 {
		t.Fatalf("List calls = %d, want 0 for non-KubeVirt session", service.listCalls)
	}
}

func TestListSessionFilesMapsBusyToTooManyRequests(t *testing.T) {
	// 사용자가 파일 새로고침을 반복해 동시성 제한에 걸리면 서버 오류가 아니라 재시도 가능한 429를 반환한다.
	service := &vmFileServiceStub{err: vmfiles.ErrBusy}
	h := vmFileHandler("ready", service)
	r := vmFileRouter(h, "alice")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sessions/s1/files", nil))

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPreviewSessionFileRequiresPath(t *testing.T) {
	// 빈 path는 snapshot 검증이나 VM read 명령까지 내려가지 않고 HTTP 계층에서 바로 거부한다.
	service := &vmFileServiceStub{}
	h := vmFileHandler("ready", service)
	r := vmFileRouter(h, "alice")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sessions/s1/files/preview", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if service.readCalls != 0 {
		t.Fatalf("Read calls = %d, want 0 without path", service.readCalls)
	}
}

func TestPreviewSessionFileReturnsPreview(t *testing.T) {
	// 정상 경로: active KubeVirt 세션의 목록 포함 파일이면 preview JSON을 Web 응답 형태로 전달한다.
	service := &vmFileServiceStub{
		preview: []byte(`{"path":"work/app.log","content":"hello\n","truncated":false}` + "\n"),
	}
	h := vmFileHandler("ready", service)
	r := vmFileRouter(h, "alice")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sessions/s1/files/preview?path=work/app.log", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if service.readPath != "work/app.log" {
		t.Fatalf("Read path = %q, want work/app.log", service.readPath)
	}
	var got struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if got.Path != "work/app.log" || got.Content != "hello\n" || got.Truncated {
		t.Fatalf("unexpected preview: %+v", got)
	}
}

func TestPreviewSessionFileMapsUnlistedFileToNotFound(t *testing.T) {
	// 추측 경로 방어: Service.Read가 snapshot에 없는 파일을 거부하면 존재 여부를 노출하지 않게 404로 매핑한다.
	service := &vmFileServiceStub{readErr: vmfiles.ErrFileNotListed}
	h := vmFileHandler("ready", service)
	r := vmFileRouter(h, "alice")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sessions/s1/files/preview?path=secret.txt", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
