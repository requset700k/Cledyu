package kubevirt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/requset700k/cledyu/api/internal/config"
)

var ErrNotFound = errors.New("session not found")

const (
	// annUserID는 세션 소유자(JWT sub)를 세션 namespace에 보관하는 annotation 키다.
	annUserID = "cledyu.io/user-id"
	// labelManagedBy/managedByValue는 세션 namespace 를 식별하는 라벨이다.
	// annotation은 label selector로 조회할 수 없으므로, 활성 세션 조회 시 이 라벨로 후보를 한정한 뒤
	// user-id annotation으로 매칭한다.
	labelManagedBy = "cledyu.io/managed-by"
	managedByValue = "cledyu-session"
)

var (
	vmGVR  = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	vmiGVR = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"}
)

// labVMType은 lab ID → instancetype 이름 매핑이다. 없는 lab은 lab-small 폴백.
var labVMType = map[string]string{
	"lab-k8s-basics":       "lab-small",
	"lab-docker-basics":    "lab-small",
	"lab-ansible-basics":   "lab-small",
	"lab-terraform-basics": "lab-small",
	"lab-helm-advanced":    "lab-medium",
}

type Session struct {
	ID        string    `json:"id"`
	LabID     string    `json:"lab_id"`
	UserID    string    `json:"user_id"`
	Status    string    `json:"status"` // provisioning | ready | failed
	StartedAt time.Time `json:"started_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Manager struct {
	core kubernetes.Interface
	dyn  dynamic.Interface
	cfg  *config.KubeVirtConfig
}

func NewManager(cfg *config.KubeVirtConfig) (*Manager, error) {
	core, dyn, err := newClients(cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubevirt manager: %w", err)
	}
	return &Manager{core: core, dyn: dyn, cfg: cfg}, nil
}

// BootInit은 랩별 cloud-init 추가 작업이다(content.InitSpec 에서 변환).
// content 패키지에 대한 역방향 의존을 피하려고 kubevirt 가 자체 타입을 가진다.
type BootInit struct {
	Packages []string // cloud-init packages: (apt 설치)
	Runcmd   []string // cloud-init runcmd: 끝에 추가되는 셸 명령
}

func (m *Manager) Create(ctx context.Context, sessionID, labID, userID string, init BootInit) (*Session, error) {
	ns := "lab-" + sessionID
	vmType := labVMType[labID]
	if vmType == "" {
		vmType = "lab-small"
	}
	now := time.Now().UTC()
	expires := now.Add(time.Duration(m.cfg.SessionTTLHours) * time.Hour)

	// 1. Namespace — 세션 메타데이터는 annotation에 보관한다.
	_, err := m.core.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: map[string]string{labelManagedBy: managedByValue},
			Annotations: map[string]string{
				"cledyu.io/lab-id":     labID,
				annUserID:              userID,
				"cledyu.io/started-at": now.Format(time.RFC3339),
				"cledyu.io/expires-at": expires.Format(time.RFC3339),
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create namespace: %w", err)
	}

	// 2. cloud-init Secret
	_, err = m.core.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-cloudinit",
			Namespace: ns,
		},
		StringData: map[string]string{
			"userdata": renderCloudInit(sessionID, m.cfg.LabSSHPublicKey, init),
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create cloud-init secret: %w", err)
	}

	// 3. VirtualMachine — dataVolumeTemplates.source.pvc로 base 이미지를 복제한다.
	vm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      "session-vm",
				"namespace": ns,
			},
			"spec": map[string]interface{}{
				"running": true,
				"instancetype": map[string]interface{}{
					"kind": "VirtualMachineClusterInstancetype",
					"name": vmType,
				},
				"preference": map[string]interface{}{
					"kind": "VirtualMachineClusterPreference",
					"name": "ubuntu",
				},
				"dataVolumeTemplates": []interface{}{
					map[string]interface{}{
						"metadata": map[string]interface{}{
							"name": "session-rootdisk",
						},
						"spec": map[string]interface{}{
							"source": map[string]interface{}{
								"pvc": map[string]interface{}{
									"namespace": m.cfg.BaseImageNS,
									"name":      m.cfg.BaseImageName,
								},
							},
							"storage": map[string]interface{}{
								"accessModes": []interface{}{"ReadWriteOnce"},
								"resources": map[string]interface{}{
									"requests": map[string]interface{}{
										"storage": "10Gi",
									},
								},
								"storageClassName": m.cfg.StorageClass,
							},
						},
					},
				},
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"terminationGracePeriodSeconds": int64(30),
						"domain": map[string]interface{}{
							"devices": map[string]interface{}{
								"disks": []interface{}{
									map[string]interface{}{
										"name": "rootdisk",
										"disk": map[string]interface{}{"bus": "virtio"},
									},
									map[string]interface{}{
										"name": "cloudinitdisk",
										"disk": map[string]interface{}{"bus": "virtio"},
									},
								},
								"interfaces": []interface{}{
									map[string]interface{}{
										"name":       "default",
										"masquerade": map[string]interface{}{},
									},
								},
							},
						},
						"networks": []interface{}{
							map[string]interface{}{
								"name": "default",
								"pod":  map[string]interface{}{},
							},
						},
						"volumes": []interface{}{
							map[string]interface{}{
								"name": "rootdisk",
								"dataVolume": map[string]interface{}{
									"name": "session-rootdisk",
								},
							},
							map[string]interface{}{
								"name": "cloudinitdisk",
								"cloudInitNoCloud": map[string]interface{}{
									"secretRef": map[string]interface{}{
										"name": "session-cloudinit",
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if _, err = m.dyn.Resource(vmGVR).Namespace(ns).Create(ctx, vm, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create vm: %w", err)
	}

	return &Session{
		ID:        sessionID,
		LabID:     labID,
		UserID:    userID,
		Status:    "provisioning",
		StartedAt: now,
		ExpiresAt: expires,
	}, nil
}

// FindActiveByUser는 해당 user가 소유한 활성 세션의 sessionID를 반환한다.
// 활성 세션이 없으면 빈 문자열을 반환한다(에러 아님). 세션 namespace는 managed-by 라벨로 한정해
// 조회한 뒤 user-id annotation으로 매칭하며, 삭제 진행 중(Terminating) namespace는 곧 사라지므로 제외한다.
func (m *Manager) FindActiveByUser(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", nil
	}
	list, err := m.core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: labelManagedBy + "=" + managedByValue,
	})
	if err != nil {
		return "", fmt.Errorf("list session namespaces: %w", err)
	}
	for _, ns := range list.Items {
		if ns.Annotations[annUserID] != userID {
			continue
		}
		if ns.Status.Phase == corev1.NamespaceTerminating {
			continue
		}
		return strings.TrimPrefix(ns.Name, "lab-"), nil
	}
	return "", nil
}

// CountActiveSessions는 현재 활성(삭제 중이 아닌) 세션 namespace 수를 반환한다(동시 세션 쿼터용).
func (m *Manager) CountActiveSessions(ctx context.Context) (int, error) {
	list, err := m.core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: labelManagedBy + "=" + managedByValue,
	})
	if err != nil {
		return 0, fmt.Errorf("list session namespaces: %w", err)
	}
	n := 0
	for i := range list.Items {
		if list.Items[i].Status.Phase != corev1.NamespaceTerminating {
			n++
		}
	}
	return n, nil
}

// ReapStuckSessions는 생성 후 timeout 안에 VM이 ready(Running)가 되지 못한 세션 namespace를 삭제하고
// 회수된 sessionID 목록을 반환한다. ready(Running) 세션은 회수하지 않는다(정상 세션 보호).
// stuck provisioning이 CDI 클론 재시도 thrash로 번져 스토리지를 마르게 하는 것을 차단하기 위한 GC다.
func (m *Manager) ReapStuckSessions(ctx context.Context, timeout time.Duration) ([]string, error) {
	list, err := m.core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: labelManagedBy + "=" + managedByValue,
	})
	if err != nil {
		return nil, fmt.Errorf("list session namespaces: %w", err)
	}
	now := time.Now()
	var reaped []string
	for i := range list.Items {
		ns := &list.Items[i]
		if ns.Status.Phase == corev1.NamespaceTerminating {
			continue // 이미 삭제 중
		}
		started, _ := time.Parse(time.RFC3339, ns.Annotations["cledyu.io/started-at"])
		if started.IsZero() {
			started = ns.CreationTimestamp.Time
		}
		if now.Sub(started) < timeout {
			continue // 아직 프로비저닝 유예 시간 내
		}
		if m.vmiPhase(ctx, ns.Name) == "Running" {
			continue // ready 상태면 정상 세션 — 회수 금지
		}
		if err := m.core.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil && !k8serr.IsNotFound(err) {
			continue // best-effort — 다음 주기에 재시도
		}
		reaped = append(reaped, strings.TrimPrefix(ns.Name, "lab-"))
	}
	return reaped, nil
}

// ReapExpiredSessions는 expires_at(세션 TTL)이 지난 세션 namespace를 삭제하고 회수된
// sessionID 목록을 반환한다. ReapStuckSessions 와 달리 Running 세션도 회수 대상이다 —
// 광고한 세션 최대 수명(SessionTTLHours)을 지나면 정상 동작 중이어도 종료한다.
// 만료 enforcement 부재로 세션이 영원히 살아 온프렘 VM 풀·스토리지를 잠그는 것을 막는다.
func (m *Manager) ReapExpiredSessions(ctx context.Context) ([]string, error) {
	list, err := m.core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: labelManagedBy + "=" + managedByValue,
	})
	if err != nil {
		return nil, fmt.Errorf("list session namespaces: %w", err)
	}
	now := time.Now()
	var reaped []string
	for i := range list.Items {
		ns := &list.Items[i]
		if ns.Status.Phase == corev1.NamespaceTerminating {
			continue // 이미 삭제 중
		}
		expires, err := time.Parse(time.RFC3339, ns.Annotations["cledyu.io/expires-at"])
		if err != nil || expires.IsZero() {
			continue // 만료 시각을 못 읽으면 보수적으로 건너뛴다(레거시/손상 세션은 stuck reaper가 처리)
		}
		if now.Before(expires) {
			continue // 아직 만료 전
		}
		if err := m.core.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil && !k8serr.IsNotFound(err) {
			continue // best-effort — 다음 주기에 재시도
		}
		reaped = append(reaped, strings.TrimPrefix(ns.Name, "lab-"))
	}
	return reaped, nil
}

// vmiPhase는 세션 VM(session-vm)의 VMI phase를 반환한다(없거나 오류면 빈 문자열).
func (m *Manager) vmiPhase(ctx context.Context, ns string) string {
	vmi, err := m.dyn.Resource(vmiGVR).Namespace(ns).Get(ctx, "session-vm", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	phase, _, _ := unstructured.NestedString(vmi.Object, "status", "phase")
	return phase
}

// VMIAddress는 세션 VM 의 pod 네트워크 IP 를 반환한다(IDE 등 in-cluster HTTP 프록시용).
// VMI 가 없거나 아직 IP 를 못 받았으면 ErrNotFound 를 반환한다.
func (m *Manager) VMIAddress(ctx context.Context, sessionID string) (string, error) {
	ns := "lab-" + sessionID
	vmi, err := m.dyn.Resource(vmiGVR).Namespace(ns).Get(ctx, "session-vm", metav1.GetOptions{})
	if err != nil {
		if k8serr.IsNotFound(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("get vmi: %w", err)
	}
	ifaces, _, _ := unstructured.NestedSlice(vmi.Object, "status", "interfaces")
	for _, raw := range ifaces {
		iface, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if ip, _ := iface["ipAddress"].(string); ip != "" {
			return ip, nil
		}
	}
	return "", ErrNotFound
}

func (m *Manager) Get(ctx context.Context, sessionID string) (*Session, error) {
	ns := "lab-" + sessionID
	nsObj, err := m.core.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		if k8serr.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get namespace: %w", err)
	}

	ann := nsObj.Annotations
	startedAt, _ := time.Parse(time.RFC3339, ann["cledyu.io/started-at"])
	expiresAt, _ := time.Parse(time.RFC3339, ann["cledyu.io/expires-at"])

	status := "provisioning"
	vmi, err := m.dyn.Resource(vmiGVR).Namespace(ns).Get(ctx, "session-vm", metav1.GetOptions{})
	if err == nil {
		phase, _, _ := unstructured.NestedString(vmi.Object, "status", "phase")
		switch phase {
		case "Running":
			status = "ready"
		case "Failed", "Succeeded":
			status = "failed"
		}
	}
	if status == "provisioning" && m.provisioningTimedOut(startedAt) {
		status = "failed"
	}

	return &Session{
		ID:        sessionID,
		LabID:     ann["cledyu.io/lab-id"],
		UserID:    ann["cledyu.io/user-id"],
		Status:    status,
		StartedAt: startedAt,
		ExpiresAt: expiresAt,
	}, nil
}

func (m *Manager) provisioningTimedOut(startedAt time.Time) bool {
	if m.cfg == nil || m.cfg.ProvisionTimeoutMinutes <= 0 || startedAt.IsZero() {
		return false
	}
	timeout := time.Duration(m.cfg.ProvisionTimeoutMinutes) * time.Minute
	return time.Since(startedAt) >= timeout
}

func (m *Manager) Delete(ctx context.Context, sessionID string) error {
	ns := "lab-" + sessionID
	if err := m.core.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}); err != nil {
		if k8serr.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("delete namespace: %w", err)
	}
	return nil
}
