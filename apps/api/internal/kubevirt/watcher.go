package kubevirt

// session-vm VMI의 phase 전환을 HTTP 폴링과 무관하게 감지해서
// syncBootStatus를 호출한다. Get()이 호출되지 않아도(예: 프론트엔드가 /ws 연결 후
// 폴링을 멈춘 경우) vm_boot_total이 기록되도록 하는 것이 목적.

import (
	"context"
	"fmt"
	"time"

	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/requset700k/cledyu/api/internal/session"
	"github.com/requset700k/cledyu/api/internal/vmmetrics"
)

// StartVMIWatcher는 전체 네임스페이스의 session-vm VMI를 watch하며,
// phase 전환 즉시 syncBootStatus를 호출한다.
// resyncPeriod(30s)는 이벤트 유실 대비 안전망이고, 주 트리거는 Add/Update 이벤트다.
func (m *Manager) StartVMIWatcher(ctx context.Context) {
	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
				opts.FieldSelector = "metadata.name=session-vm"
				return m.dyn.Resource(vmiGVR).Namespace(metav1.NamespaceAll).List(ctx, opts)
			},
			WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
				opts.FieldSelector = "metadata.name=session-vm"
				return m.dyn.Resource(vmiGVR).Namespace(metav1.NamespaceAll).Watch(ctx, opts)
			},
		},
		&unstructured.Unstructured{},
		30*time.Second,
		cache.Indexers{},
	)

	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { m.handleVMIEvent(ctx, obj) },
		UpdateFunc: func(_, newObj interface{}) { m.handleVMIEvent(ctx, newObj) },
	}); err != nil {
		fmt.Printf("vmi watcher event handler 등록 실패: %v\n", err)
		return
	}

	go informer.Run(ctx.Done())
}

func (m *Manager) handleVMIEvent(ctx context.Context, obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	ns := u.GetNamespace()
	phase, found, _ := unstructured.NestedString(u.Object, "status", "phase")
	if !found || phase == "" {
		return
	}
	if err := m.syncBootStatus(ctx, ns, phase); err != nil {
		fmt.Printf("boot status sync failed (watch path): ns=%s phase=%s err=%v\n", ns, phase, err)
	}
}

// syncBootStatus는 namespace annotation을 기준으로 boot 결과를 "1회만" 기록하고,
// 그 시점에 한해 vm_boot_total 카운터를 올린다.
// Get()의 폴링 경로와 watcher의 이벤트 경로 양쪽에서 공유하는 유일한 기록 지점이다.
// phase는 "Running" | "Failed" | "Succeeded" | "TimedOut"(provisioningTimedOut에서 호출) 중 하나.
func (m *Manager) syncBootStatus(ctx context.Context, ns string, phase string) error {
	nsObj, err := m.core.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		if k8serr.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get namespace: %w", err)
	}

	ann := nsObj.Annotations
	if ann == nil {
		ann = map[string]string{}
		nsObj.Annotations = ann
	}
	startedAt, _ := time.Parse(time.RFC3339, ann["cledyu.io/started-at"])

	switch phase {
	case "Running":
		if ann["cledyu.io/ready-at"] != "" {
			return nil // 이미 기록됨
		}
		nsObj.Annotations["cledyu.io/ready-at"] = time.Now().UTC().Format(time.RFC3339)
		if _, err := m.core.CoreV1().Namespaces().Update(ctx, nsObj, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update ready-at annotation: %w", err)
		}
		if !startedAt.IsZero() {
			m.met.RecordBoot(vmmetrics.ResultSuccess, session.ProviderKubeVirt)
		}

	case "Failed", "Succeeded", "TimedOut":
		// 이미 ready였던 VM의 사후 종료는 실패로 재집계하지 않음
		if ann["cledyu.io/ready-at"] != "" || ann["cledyu.io/boot-result-recorded"] != "" {
			return nil
		}
		nsObj.Annotations["cledyu.io/boot-result-recorded"] = "true"
		if _, err := m.core.CoreV1().Namespaces().Update(ctx, nsObj, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update boot-result-recorded annotation: %w", err)
		}
		m.met.RecordBoot(vmmetrics.ResultFailed, session.ProviderKubeVirt)
	}

	return nil
}
