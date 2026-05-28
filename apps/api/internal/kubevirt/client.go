package kubevirt

import (
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// newClients는 InCluster 설정을 먼저 시도하고, 실패하면 kubeconfig 파일을 사용한다.
func newClients(kubeconfig string) (kubernetes.Interface, dynamic.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, nil, err
		}
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	return core, dyn, nil
}
