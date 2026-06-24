package handlers

import (
	"testing"
	"time"

	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"github.com/requset700k/cledyu/api/internal/session"
	"go.uber.org/zap"
)

func TestSessionResponseOmitsTerminalURLUntilReady(t *testing.T) {
	h := &Handler{
		labs: map[string]content.LabContent{
			"lab-k8s-basics": {ID: "lab-k8s-basics", Environment: "ubuntu"},
		},
		steps: newStepStore(nil, zap.NewNop()),
	}
	base := kubevirt.Session{
		ID:        "abc123",
		LabID:     "lab-k8s-basics",
		UserID:    "alice",
		StartedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	failed := base
	failed.Status = "failed"
	got := h.sessionResponse(&failed)
	if _, ok := got["terminal_url"]; ok {
		t.Fatalf("terminal_url should be omitted for failed session: %#v", got)
	}

	ready := base
	ready.Status = "ready"
	got = h.sessionResponse(&ready)
	if got["terminal_url"] != "/api/v1/sessions/abc123/ws" {
		t.Fatalf("terminal_url = %v, want ready terminal URL", got["terminal_url"])
	}
}

// EC2 세션은 tailnet 가입(authkey 설정) 시에만 라이브 터미널에 도달 가능하므로,
// authkey 미설정이면 terminal_url 을 광고하지 않는다(깨진 터미널 탭 방지).
func TestSessionResponseEC2TerminalGatedByTailnet(t *testing.T) {
	mk := func(authkey string) *Handler {
		return &Handler{
			cfg:   &config.Config{AWS: config.AWSConfig{TailscaleAuthKey: authkey}},
			labs:  map[string]content.LabContent{"lab-k8s-basics": {ID: "lab-k8s-basics", Environment: "ubuntu"}},
			steps: newStepStore(nil, zap.NewNop()),
		}
	}
	sess := &session.Session{
		ID: "e1", LabID: "lab-k8s-basics", UserID: "alice", Status: "ready",
		Provider: session.ProviderEC2, StartedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}

	if got := mk("").sessionResponse(sess); got["terminal_url"] != nil {
		t.Fatalf("EC2 without tailnet should omit terminal_url: %#v", got)
	}
	if got := mk("tskey-auth-x").sessionResponse(sess); got["terminal_url"] != "/api/v1/sessions/e1/ws" {
		t.Fatalf("EC2 with tailnet should advertise terminal_url, got %v", got["terminal_url"])
	}
}
