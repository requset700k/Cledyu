package kubevirt

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// expiringNS는 expires-at annotation 을 가진 세션 namespace 를 만든다.
func expiringNS(id string, expiresAt time.Time, phase corev1.NamespacePhase) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "lab-" + id,
			Labels: map[string]string{labelManagedBy: managedByValue},
			Annotations: map[string]string{
				annUserID:              "alice",
				"cledyu.io/expires-at": expiresAt.Format(time.RFC3339),
			},
		},
		Status: corev1.NamespaceStatus{Phase: phase},
	}
}

func nsExists(t *testing.T, m *Manager, id string) bool {
	t.Helper()
	_, err := m.core.CoreV1().Namespaces().Get(context.Background(), "lab-"+id, metav1.GetOptions{})
	return err == nil
}

func TestReapExpiredSessions(t *testing.T) {
	now := time.Now()
	m := newTestManager(
		expiringNS("expired1", now.Add(-time.Minute), corev1.NamespaceActive),       // 만료 → 회수
		expiringNS("expired2", now.Add(-3*time.Hour), corev1.NamespaceActive),       // 한참 만료 → 회수
		expiringNS("live", now.Add(time.Hour), corev1.NamespaceActive),              // 아직 유효 → 보존
		expiringNS("terminating", now.Add(-time.Hour), corev1.NamespaceTerminating), // 삭제 중 → 건너뜀
		// expires-at 없는 레거시 세션은 보수적으로 보존(annUserID 만).
		sessionNS("legacy", "bob", corev1.NamespaceActive),
	)

	reaped, err := m.ReapExpiredSessions(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := map[string]bool{}
	for _, id := range reaped {
		got[id] = true
	}
	if !got["expired1"] || !got["expired2"] {
		t.Errorf("expected expired1, expired2 reaped, got %v", reaped)
	}
	if got["live"] || got["legacy"] || got["terminating"] {
		t.Errorf("reaped a session that should be preserved: %v", reaped)
	}

	// 실제 namespace 삭제까지 확인.
	if nsExists(t, m, "expired1") || nsExists(t, m, "expired2") {
		t.Error("expired namespaces must be deleted")
	}
	if !nsExists(t, m, "live") || !nsExists(t, m, "legacy") {
		t.Error("valid/legacy namespaces must be preserved")
	}
}

// 만료 시각이 손상/누락이면 회수하지 않는다(보수적).
func TestReapExpiredSessions_BadAnnotation(t *testing.T) {
	ns := expiringNS("broken", time.Now().Add(-time.Hour), corev1.NamespaceActive)
	ns.Annotations["cledyu.io/expires-at"] = "not-a-timestamp"
	m := newTestManager(ns)

	reaped, err := m.ReapExpiredSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 0 {
		t.Errorf("malformed expires-at must not be reaped, got %v", reaped)
	}
}

var _ runtime.Object = (*corev1.Namespace)(nil)
