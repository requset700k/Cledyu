package handlers

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/ec2"
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

func TestSessionResponseIncludesProvisioningStage(t *testing.T) {
	h := &Handler{
		steps: newStepStore(nil, zap.NewNop()),
	}
	sess := &session.Session{
		ID:                "abc123",
		LabID:             "lab-k8s-basics",
		UserID:            "alice",
		Status:            "provisioning",
		ProvisioningStage: "disk_cloning",
		StartedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(time.Hour),
		Provider:          session.ProviderKubeVirt,
	}

	got := h.sessionResponse(sess)
	if got["provisioning_stage"] != "disk_cloning" {
		t.Fatalf("provisioning_stage = %v, want disk_cloning", got["provisioning_stage"])
	}
}

// EC2 라이브 터미널 광고는 (1) 세션 인스턴스의 실제 tailnet 가입(session.TailnetEnabled — 정적
// 키 유무가 아니라 해당 세션이 실제 가입했는지)과 (2) api 자신의 tsnet 가입(ec2Dial 주입) 둘 다
// 있어야 한다. 하나라도 없으면 /ws 접속이 깨지므로 광고하지 않는다.
func TestSessionResponseEC2TerminalGatedByTailnet(t *testing.T) {
	noopDial := func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	mk := func(dial ec2.DialFunc) *Handler {
		return &Handler{
			cfg:     &config.Config{},
			labs:    map[string]content.LabContent{"lab-k8s-basics": {ID: "lab-k8s-basics", Environment: "ubuntu"}},
			steps:   newStepStore(nil, zap.NewNop()),
			ec2Dial: dial,
		}
	}
	sess := func(tailnet bool) *session.Session {
		return &session.Session{
			ID: "e1", LabID: "lab-k8s-basics", UserID: "alice", Status: "ready",
			Provider: session.ProviderEC2, StartedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
			TailnetEnabled: tailnet,
		}
	}

	// 세션이 tailnet 미가입(발급 실패/기능 off) → 광고 안 함.
	if got := mk(noopDial).sessionResponse(sess(false)); got["terminal_url"] != nil {
		t.Fatalf("EC2 session not on tailnet should omit terminal_url: %#v", got)
	}
	// 세션은 가입했으나 api tsnet 미가입(ec2Dial nil) → 광고 안 함(깨진 탭 방지).
	if got := mk(nil).sessionResponse(sess(true)); got["terminal_url"] != nil {
		t.Fatalf("EC2 without api tsnet (nil ec2Dial) should omit terminal_url: %#v", got)
	}
	// 둘 다 있으면 광고.
	if got := mk(noopDial).sessionResponse(sess(true)); got["terminal_url"] != "/api/v1/sessions/e1/ws" {
		t.Fatalf("EC2 with tailnet+tsnet should advertise terminal_url, got %v", got["terminal_url"])
	}
}
