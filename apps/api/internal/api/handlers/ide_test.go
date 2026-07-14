package handlers

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/session"
	"go.uber.org/zap"
)

type ideSessionProvider struct {
	sess *session.Session
	addr string
}

func (p *ideSessionProvider) Create(context.Context, string, string, string, session.BootInit) (*session.Session, error) {
	return nil, nil
}
func (p *ideSessionProvider) Get(context.Context, string) (*session.Session, error) {
	return p.sess, nil
}
func (p *ideSessionProvider) Delete(context.Context, string) error { return nil }
func (p *ideSessionProvider) FindActiveByUser(context.Context, string) (string, error) {
	return "", nil
}
func (p *ideSessionProvider) CountActiveSessions(context.Context) (int, error) { return 0, nil }
func (p *ideSessionProvider) ReapStuckSessions(context.Context, time.Duration) ([]string, error) {
	return nil, nil
}
func (p *ideSessionProvider) ReapExpiredSessions(context.Context) ([]string, error) { return nil, nil }
func (p *ideSessionProvider) VMIAddress(context.Context, string) (string, error)    { return p.addr, nil }
func (p *ideSessionProvider) Capacity() int                                         { return 0 }

func ideRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Any("/api/v1/sessions/:id/ide/*idepath", func(c *gin.Context) {
		c.Set("user_id", "alice")
		h.IDE(c)
	})
	return r
}

func ideSession(provider string) *session.Session {
	return &session.Session{
		ID: "s1", LabID: "lab-ide", UserID: "alice", Status: "ready",
		Provider: provider, TailnetEnabled: true,
		StartedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
}

// EC2 세션의 code-server 는 tailnet MagicDNS 로만 닿으므로 IDE 리버스 프록시가 ec2Dial(tsnet
// 다이얼러)로 접속해야 한다. VMIAddress 가 준 MagicDNS 호스트는 기본 net.Dialer 로는 못 닿으니,
// 프록시가 백엔드에 도달했다면 ec2Dial 을 쓴 것이다(Codex: IDE 만 ec2Dial 이 빠져 있어 503 이었다).
func TestIDEProxyEC2UsesTsnetDialer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("code-server-ok:" + r.URL.Path))
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "http://")

	var dialCalls int32
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		atomic.AddInt32(&dialCalls, 1) // addr(=lab-s1:13337) 무시하고 백엔드로 — ec2Dial 사용 증명
		return (&net.Dialer{}).DialContext(ctx, network, backendAddr)
	}

	h := &Handler{
		log:      zap.NewNop(),
		labs:     map[string]content.LabContent{"lab-ide": {IDE: true}},
		sessions: &ideSessionProvider{sess: ideSession(session.ProviderEC2), addr: "lab-s1"},
		ec2Dial:  dial,
	}
	// 실 http 서버로 구동한다 — ReverseProxy 는 CloseNotifier 등 실 ResponseWriter 인터페이스를 쓴다.
	front := httptest.NewServer(ideRouter(h))
	defer front.Close()

	resp, err := http.Get(front.URL + "/api/v1/sessions/s1/ide/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("EC2 IDE proxy status = %d, want 200 (ec2Dial 로 백엔드 도달): %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "code-server-ok") {
		t.Fatalf("백엔드 응답 미도달: %q", body)
	}
	if atomic.LoadInt32(&dialCalls) == 0 {
		t.Fatal("ec2Dial 이 호출되지 않음 — IDE 프록시가 tsnet 다이얼러를 안 씀")
	}
}

// KubeVirt 세션의 ip 는 클러스터 pod IP 라 기본 Transport 로 닿아야 하며, ec2Dial(tsnet)을 쓰면
// 오히려 못 닿는다. IDE 프록시는 KubeVirt 경로에서 ec2Dial 을 절대 쓰지 않아야 한다.
func TestIDEProxyKubeVirtDoesNotUseTsnetDialer(t *testing.T) {
	var dialCalls int32
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		atomic.AddInt32(&dialCalls, 1)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	h := &Handler{
		log:      zap.NewNop(),
		labs:     map[string]content.LabContent{"lab-ide": {IDE: true}},
		sessions: &ideSessionProvider{sess: ideSession(session.ProviderKubeVirt), addr: "127.0.0.1"},
		ec2Dial:  dial, // 주입돼 있어도 KubeVirt 경로에선 쓰이면 안 됨
	}
	front := httptest.NewServer(ideRouter(h))
	defer front.Close()

	resp, err := http.Get(front.URL + "/api/v1/sessions/s1/ide/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()

	if atomic.LoadInt32(&dialCalls) != 0 {
		t.Fatalf("KubeVirt IDE 프록시가 ec2Dial 을 사용함(%d회) — 기본 Transport 여야 한다", dialCalls)
	}
}
