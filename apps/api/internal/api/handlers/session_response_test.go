package handlers

import (
	"testing"
	"time"

	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
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
