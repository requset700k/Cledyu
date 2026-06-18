package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/requset700k/cledyu/api/internal/kubevirt"
)

type failedSessionCleanerStub struct {
	existingID string
	session    *kubevirt.Session
	getErr     error
	deleteErr  error
	deletedID  string
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
