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

func (m *Manager) Create(ctx context.Context, sessionID, labID, userID string) (*Session, error) {
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

	// 검증엔진(virtctl ssh)이 키로 접속할 수 있도록 lab 사용자에 엔진 공개키를 넣는다.
	// 공개키 미설정 시 비번/시리얼 콘솔만 유지(키 블록 생략).
	sshKeyBlock := ""
	if m.cfg.LabSSHPublicKey != "" {
		sshKeyBlock = "\n    ssh_authorized_keys:\n      - " + m.cfg.LabSSHPublicKey
	}

	// 2. cloud-init Secret
	_, err = m.core.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-cloudinit",
			Namespace: ns,
		},
		StringData: map[string]string{
			"userdata": fmt.Sprintf(`#cloud-config
hostname: %s
ssh_pwauth: true
users:
  - name: lab
    lock_passwd: false
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL%s
chpasswd:
  expire: false
  list: |
    lab:lab
write_files:
  - path: /etc/systemd/system/serial-getty@ttyS0.service.d/override.conf
    permissions: "0644"
    content: |
      [Service]
      ExecStart=
      ExecStart=-/sbin/agetty --autologin lab --noclear --keep-baud 115200,38400,9600 ttyS0 xterm-256color
runcmd:
  - systemctl daemon-reload
  - systemctl restart serial-getty@ttyS0.service
`, "session-"+sessionID, sshKeyBlock),
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

	return &Session{
		ID:        sessionID,
		LabID:     ann["cledyu.io/lab-id"],
		UserID:    ann["cledyu.io/user-id"],
		Status:    status,
		StartedAt: startedAt,
		ExpiresAt: expiresAt,
	}, nil
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
