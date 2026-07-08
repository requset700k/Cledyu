package kubevirt

import (
	"context"
	"errors"
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
	k8stesting "k8s.io/client-go/testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/vmmetrics"
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

func deletingSessionNS(id, userID string) *corev1.Namespace {
	ns := sessionNS(id, userID, corev1.NamespaceActive)
	now := metav1.Now()
	ns.DeletionTimestamp = &now
	ns.Finalizers = []string{"kubernetes"}
	return ns
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
			name:   "ignores namespace with deletion timestamp",
			objs:   []runtime.Object{deletingSessionNS("abc123", "alice")},
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
		deletingSessionNS("d", "u4"),                      // 삭제 요청됨 → 제외
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

func rootDiskPVC(id string, phase corev1.PersistentVolumeClaimPhase) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "session-rootdisk", Namespace: "lab-" + id},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: phase},
	}
}

func TestGetReportsDiskCloneProvisioningStage(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vmiGVR: "VirtualMachineInstanceList"},
	)
	m := &Manager{
		core: fake.NewSimpleClientset(
			sessionNSWithTimes("clone", time.Now()),
			rootDiskPVC("clone", corev1.ClaimPending),
		),
		dyn: dyn,
		cfg: &config.KubeVirtConfig{ProvisionTimeoutMinutes: 10},
	}

	got, err := m.Get(context.Background(), "clone")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "provisioning" {
		t.Fatalf("status = %q, want provisioning", got.Status)
	}
	if got.ProvisioningStage != "disk_cloning" {
		t.Fatalf("provisioning stage = %q, want disk_cloning", got.ProvisioningStage)
	}
}

func TestGetReportsVMStartingProvisioningStage(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vmiGVR: "VirtualMachineInstanceList"},
		vmiObj("boot", "Scheduling"),
	)
	m := &Manager{
		core: fake.NewSimpleClientset(
			sessionNSWithTimes("boot", time.Now()),
			rootDiskPVC("boot", corev1.ClaimBound),
		),
		dyn: dyn,
		cfg: &config.KubeVirtConfig{ProvisionTimeoutMinutes: 10},
	}

	got, err := m.Get(context.Background(), "boot")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "provisioning" {
		t.Fatalf("status = %q, want provisioning", got.Status)
	}
	if got.ProvisioningStage != "vm_starting" {
		t.Fatalf("provisioning stage = %q, want vm_starting", got.ProvisioningStage)
	}
}

func TestReapStuckSessions(t *testing.T) {
	reg := prometheus.NewRegistry()
	met := vmmetrics.New(reg)
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
		met: met,
	}

	reaped, err := m.ReapStuckSessions(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatalf("ReapStuckSessions: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != "stuck" {
		t.Fatalf("reaped = %v, want [stuck]", reaped)
	}
	if got := testutil.ToFloat64(met.LabStartCollector().WithLabelValues(vmmetrics.ResultFailed, vmmetrics.LabEnvOnprem, vmmetrics.LabReasonTimeout)); got != 1 {
		t.Errorf("lab_start_total timeout = %v, want 1", got)
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

func TestReapStuckSessionsDoesNotDuplicateMetricsAfterDeleteRetry(t *testing.T) {
	reg := prometheus.NewRegistry()
	met := vmmetrics.New(reg)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vmiGVR: "VirtualMachineInstanceList"},
	)
	core := fake.NewSimpleClientset(reapNS("stuck", 20*time.Minute))
	deleteAttempts := 0
	core.PrependReactor("delete", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		deleteAttempts++
		if deleteAttempts == 1 {
			return true, nil, errors.New("temporary delete failure")
		}
		return false, nil, nil
	})
	m := &Manager{core: core, dyn: dyn, met: met}

	reaped, err := m.ReapStuckSessions(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatalf("ReapStuckSessions(first): %v", err)
	}
	if len(reaped) != 0 {
		t.Fatalf("first reaped = %v, want []", reaped)
	}
	if got := testutil.ToFloat64(met.LabStartCollector().WithLabelValues(vmmetrics.ResultFailed, vmmetrics.LabEnvOnprem, vmmetrics.LabReasonTimeout)); got != 1 {
		t.Fatalf("first lab_start_total timeout = %v, want 1", got)
	}

	reaped, err = m.ReapStuckSessions(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatalf("ReapStuckSessions(second): %v", err)
	}
	if len(reaped) != 1 || reaped[0] != "stuck" {
		t.Fatalf("second reaped = %v, want [stuck]", reaped)
	}
	if got := testutil.ToFloat64(met.LabStartCollector().WithLabelValues(vmmetrics.ResultFailed, vmmetrics.LabEnvOnprem, vmmetrics.LabReasonTimeout)); got != 1 {
		t.Errorf("retry duplicated lab_start_total timeout = %v, want 1", got)
	}
}

func sessionNSWithTimes(id string, startedAt time.Time) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "lab-" + id,
		Labels: map[string]string{labelManagedBy: managedByValue},
		Annotations: map[string]string{
			annUserID:              "alice",
			"cledyu.io/lab-id":     "lab-k8s-basics",
			"cledyu.io/started-at": startedAt.UTC().Format(time.RFC3339),
			"cledyu.io/expires-at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}}
}

func TestGetMarksProvisioningSessionFailedAfterTimeout(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vmiGVR: "VirtualMachineInstanceList"},
		vmiObj("timeout", "Pending"),
	)
	m := &Manager{
		core: fake.NewSimpleClientset(sessionNSWithTimes("timeout", time.Now().Add(-3*time.Minute))),
		dyn:  dyn,
		cfg:  &config.KubeVirtConfig{ProvisionTimeoutMinutes: 2},
	}

	got, err := m.Get(context.Background(), "timeout")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
}

func TestGetKeepsProvisioningSessionBeforeTimeout(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vmiGVR: "VirtualMachineInstanceList"},
		vmiObj("young", "Pending"),
	)
	m := &Manager{
		core: fake.NewSimpleClientset(sessionNSWithTimes("young", time.Now().Add(-90*time.Second))),
		dyn:  dyn,
		cfg:  &config.KubeVirtConfig{ProvisionTimeoutMinutes: 2},
	}

	got, err := m.Get(context.Background(), "young")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "provisioning" {
		t.Fatalf("status = %q, want provisioning", got.Status)
	}
}

func TestVMBootFailedRecordedOnce(t *testing.T) {
	reg := prometheus.NewRegistry()
	met := vmmetrics.New(reg)

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vmiGVR: "VirtualMachineInstanceList"},
		vmiObj("sess2", "Failed"),
	)
	m := &Manager{
		core: fake.NewSimpleClientset(sessionNSWithTimes("sess2", time.Now().Add(-30*time.Second))),
		dyn:  dyn,
		cfg:  &config.KubeVirtConfig{ProvisionTimeoutMinutes: 10},
		met:  met,
	}

	sess, err := m.Get(context.Background(), "sess2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sess.Status != "failed" {
		t.Fatalf("status = %q, want failed", sess.Status)
	}
	if got := testutil.ToFloat64(met.Collector().WithLabelValues(vmmetrics.ResultFailed, "kubevirt")); got != 1 {
		t.Errorf("실패 메트릭 = %v, want 1", got)
	}
	if got := testutil.ToFloat64(met.LabStartCollector().WithLabelValues(vmmetrics.ResultFailed, vmmetrics.LabEnvOnprem, vmmetrics.LabReasonVMFailed)); got != 1 {
		t.Errorf("lab start 실패 메트릭 = %v, want 1", got)
	}

	m.Get(context.Background(), "sess2")
	if got := testutil.ToFloat64(met.Collector().WithLabelValues(vmmetrics.ResultFailed, "kubevirt")); got != 1 {
		t.Errorf("중복 기록됨: 실패 메트릭 = %v, want 1", got)
	}
	if got := testutil.ToFloat64(met.LabStartCollector().WithLabelValues(vmmetrics.ResultFailed, vmmetrics.LabEnvOnprem, vmmetrics.LabReasonVMFailed)); got != 1 {
		t.Errorf("중복 기록됨: lab start 실패 메트릭 = %v, want 1", got)
	}
}
