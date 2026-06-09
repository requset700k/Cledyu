package kubevirt

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

func TestCountActiveSessions(t *testing.T) {
	m := newTestManager(
		sessionNS("a", "u1", corev1.NamespaceActive),
		sessionNS("b", "u2", corev1.NamespaceActive),
		sessionNS("c", "u3", corev1.NamespaceTerminating), // 삭제 중 → 제외
	)
	n, err := m.CountActiveSessions(context.Background())
	if err != nil {
		t.Fatalf("CountActiveSessions: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}

// reapNS는 started-at이 ago 전인 세션 namespace를 만든다.
func reapNS(id string, ago time.Duration) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "lab-" + id,
		Labels:      map[string]string{labelManagedBy: managedByValue},
		Annotations: map[string]string{"cledyu.io/started-at": time.Now().Add(-ago).Format(time.RFC3339)},
	}}
}

func vmiObj(id, phase string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachineInstance",
		"metadata":   map[string]interface{}{"name": "session-vm", "namespace": "lab-" + id},
		"status":     map[string]interface{}{"phase": phase},
	}}
}

func TestReapStuckSessions(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vmiGVR: "VirtualMachineInstanceList"},
		vmiObj("ready", "Running"), // ready 세션의 VMI
		vmiObj("young", "Pending"), // 최근 세션의 VMI
	)
	m := &Manager{
		core: fake.NewSimpleClientset(
			reapNS("stuck", 20*time.Minute), // 오래됨 + VMI 없음 → 회수
			reapNS("ready", 20*time.Minute), // 오래됨 but Running → 보존
			reapNS("young", 1*time.Minute),  // 유예 시간 내 → 보존
		),
		dyn: dyn,
	}

	reaped, err := m.ReapStuckSessions(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatalf("ReapStuckSessions: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != "stuck" {
		t.Fatalf("reaped = %v, want [stuck]", reaped)
	}
	// stuck namespace는 삭제됐어야 한다.
	if _, err := m.core.CoreV1().Namespaces().Get(context.Background(), "lab-stuck", metav1.GetOptions{}); !k8serr.IsNotFound(err) {
		t.Errorf("lab-stuck should be deleted, got err=%v", err)
	}
	// ready/young은 보존됐어야 한다.
	for _, keep := range []string{"lab-ready", "lab-young"} {
		if _, err := m.core.CoreV1().Namespaces().Get(context.Background(), keep, metav1.GetOptions{}); err != nil {
			t.Errorf("%s should remain, got err=%v", keep, err)
		}
	}
}
