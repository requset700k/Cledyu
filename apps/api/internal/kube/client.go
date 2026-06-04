// Package kube는 KubeVirt 클라이언트 초기화를 담당한다.
// Phase-1에서는 lab VM의 serial console 서브리소스에 접근하는 용도로만 사용한다.
package kube

import (
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"kubevirt.io/client-go/kubecli"
)

// NewKubevirtClient는 KubeVirt 클라이언트를 생성한다.
// 우선 in-cluster 설정을 시도하고(파드 내 ServiceAccount), 실패하면 로컬 kubeconfig
// (KUBECONFIG 또는 ~/.kube/config)로 폴백한다 — 로컬 개발 시 사용.
func NewKubevirtClient() (kubecli.KubevirtClient, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
		cfg, err = clientConfig.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("load kube config (in-cluster and kubeconfig both failed): %w", err)
		}
	}
	return kubecli.GetKubevirtClientFromRESTConfig(cfg)
}
