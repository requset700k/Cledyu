package kubevirt

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// sessionNS는 Create가 만드는 세션 namespace와 동일한 라벨/annotation을 가진 테스트 객체를 만든다.
func sessionNS(id, userID string, phase corev1.NamespacePhase) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "lab-" + id,
			Labels:      map[string]string{labelManagedBy: managedByValue},
			Annotations: map[string]string{annUserID: userID},
		},
		Status: corev1.NamespaceStatus{Phase: phase},
	}
}

func newTestManager(objs ...runtime.Object) *Manager {
	return &Manager{core: fake.NewSimpleClientset(objs...)}
}

func TestFindActiveByUser(t *testing.T) {
	tests := []struct {
		name   string
		objs   []runtime.Object
		userID string
		want   string
	}{
		{
			name:   "no sessions",
			userID: "alice",
			want:   "",
		},
		{
			name:   "matches own active session",
			objs:   []runtime.Object{sessionNS("abc123", "alice", corev1.NamespaceActive)},
			userID: "alice",
			want:   "abc123",
		},
		{
			name: "ignores other users sessions",
			objs: []runtime.Object{
				sessionNS("abc123", "bob", corev1.NamespaceActive),
				sessionNS("def456", "carol", corev1.NamespaceActive),
			},
			userID: "alice",
			want:   "",
		},
		{
			name:   "ignores terminating namespace",
			objs:   []runtime.Object{sessionNS("abc123", "alice", corev1.NamespaceTerminating)},
			userID: "alice",
			want:   "",
		},
		{
			name:   "empty userID matches nothing",
			objs:   []runtime.Object{sessionNS("abc123", "", corev1.NamespaceActive)},
			userID: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestManager(tt.objs...)
			got, err := m.FindActiveByUser(context.Background(), tt.userID)
			if err != nil {
				t.Fatalf("FindActiveByUser: %v", err)
			}
			if got != tt.want {
				t.Errorf("FindActiveByUser = %q, want %q", got, tt.want)
			}
		})
	}
}
