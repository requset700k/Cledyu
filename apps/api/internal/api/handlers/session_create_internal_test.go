package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/requset700k/cledyu/api/internal/kubevirt"
)

// failedSessionCleanerStub은 sessionCreationManager의 테스트용 stub이다.
// 실제 KubeVirt 클러스터 없이 FindActiveByUser/Get/Delete 응답을 고정해
// prepareSessionCreation의 분기를 단위 테스트한다.
type failedSessionCleanerStub struct {
	existingID string            // FindActiveByUser가 돌려줄 기존 활성 세션 ID
	session    *kubevirt.Session // Get이 돌려줄 세션(상태에 따라 분기 결정)
	getErr     error             // Get에서 강제할 에러(미발견 등 시뮬레이션용)
	deleteErr  error             // Delete에서 강제할 에러
	deletedID  string            // Delete가 실제로 호출된 세션 ID(호출 여부 검증용)
}

func (s *failedSessionCleanerStub) FindActiveByUser(context.Context, string) (string, error) {
	return s.existingID, nil
}

func (s *failedSessionCleanerStub) Get(context.Context, string) (*kubevirt.Session, error) {
	return s.session, s.getErr
}

func (s *failedSessionCleanerStub) Delete(_ context.Context, sessionID string) error {
	s.deletedID = sessionID
	return s.deleteErr
}

// 기존 세션이 failed 상태면 namespace를 즉시 삭제하고(reaper를 기다리지 않음)
// existing을 비워 CreateSession이 곧바로 새 세션을 만들 수 있게 한다.
func TestPrepareSessionCreationDeletesFailedSession(t *testing.T) {
	stub := &failedSessionCleanerStub{
		existingID: "failed-session",
		session: &kubevirt.Session{
			ID:        "failed-session",
			Status:    "failed",
			StartedAt: time.Now().Add(-11 * time.Minute),
		},
	}

	existing, cleaned, err := prepareSessionCreation(context.Background(), stub, "alice")
	if err != nil {
		t.Fatalf("prepareSessionCreation() error = %v", err)
	}
	if existing != "" {
		t.Fatalf("existing session = %q, want empty", existing)
	}
	if cleaned != "failed-session" {
		t.Fatalf("cleaned session = %q, want failed-session", cleaned)
	}
	if stub.deletedID != "failed-session" {
		t.Fatalf("deleted session = %q, want failed-session", stub.deletedID)
	}
}

// 기존 세션이 아직 provisioning 중이면 삭제하지 않고 existing을 그대로 돌려줘
// CreateSession이 409(session_exists)로 거부하게 한다 — 정상 진행 중 세션 보호.
func TestPrepareSessionCreationKeepsProvisioningSession(t *testing.T) {
	stub := &failedSessionCleanerStub{
		existingID: "provisioning-session",
		session: &kubevirt.Session{
			ID:     "provisioning-session",
			Status: "provisioning",
		},
	}

	existing, cleaned, err := prepareSessionCreation(context.Background(), stub, "alice")
	if err != nil {
		t.Fatalf("prepareSessionCreation() error = %v", err)
	}
	if existing != "provisioning-session" {
		t.Fatalf("existing session = %q, want provisioning-session", existing)
	}
	if cleaned != "" {
		t.Fatalf("cleaned session = %q, want empty", cleaned)
	}
	if stub.deletedID != "" {
		t.Fatalf("deleted session = %q, want empty", stub.deletedID)
	}
}

// failed 세션을 정리하다 Delete가 실패하면 그 에러를 그대로 전파해야 한다 —
// 삭제 실패를 삼키고 진행하면 사용자가 같은 failed namespace로 다시 묶일 수 있다.
func TestPrepareSessionCreationReturnsDeleteFailure(t *testing.T) {
	deleteErr := errors.New("delete failed")
	stub := &failedSessionCleanerStub{
		existingID: "failed-session",
		session:    &kubevirt.Session{ID: "failed-session", Status: "failed"},
		deleteErr:  deleteErr,
	}

	_, _, err := prepareSessionCreation(context.Background(), stub, "alice")
	if !errors.Is(err, deleteErr) {
		t.Fatalf("prepareSessionCreation() error = %v, want %v", err, deleteErr)
	}
}
