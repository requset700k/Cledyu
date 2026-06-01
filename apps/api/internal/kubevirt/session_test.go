package kubevirt

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/requset700k/cledyu/api/internal/config"
)

func newTestManager() *Manager {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		vmGVR:  "VirtualMachineList",
		vmiGVR: "VirtualMachineInstanceList",
	})
	return &Manager{
		core: k8sfake.NewSimpleClientset(),
		dyn:  dyn,
		cfg: &config.KubeVirtConfig{
			BaseImageNS:     "kubevirt",
			BaseImageName:   "ubuntu-base",
			SessionTTLHours: 1,
		},
	}
}

// Create 는 세션 네임스페이스(lab-{sessionID})에 validation-engine 이 그 세션 VM 에만
// 접근하도록 namespaced RoleBinding 을 만들어야 한다(공유 ClusterRole 참조, per-session 격리).
func TestCreateGrantsValidationEngineRoleBindingInSessionNamespace(t *testing.T) {
	m := newTestManager()
	ctx := context.Background()

	if _, err := m.Create(ctx, "abc123", "lab-k8s-basics", "user1"); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	rb, err := m.core.RbacV1().RoleBindings("lab-abc123").Get(ctx, "validation-engine", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected RoleBinding 'validation-engine' in ns lab-abc123: %v", err)
	}

	if rb.RoleRef.Kind != "ClusterRole" {
		t.Errorf("RoleRef.Kind = %q, want ClusterRole", rb.RoleRef.Kind)
	}
	if rb.RoleRef.Name != "validation-engine" {
		t.Errorf("RoleRef.Name = %q, want validation-engine", rb.RoleRef.Name)
	}
	if rb.RoleRef.APIGroup != "rbac.authorization.k8s.io" {
		t.Errorf("RoleRef.APIGroup = %q, want rbac.authorization.k8s.io", rb.RoleRef.APIGroup)
	}

	if len(rb.Subjects) != 1 {
		t.Fatalf("len(Subjects) = %d, want 1", len(rb.Subjects))
	}
	s := rb.Subjects[0]
	if s.Kind != "ServiceAccount" || s.Name != "validation-engine" || s.Namespace != "validation-engine" {
		t.Errorf("Subject = %+v, want ServiceAccount validation-engine/validation-engine", s)
	}
}
