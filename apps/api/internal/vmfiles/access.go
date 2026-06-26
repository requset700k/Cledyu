package vmfiles

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/rest"
)

// NewKubeVirtFileListRunner는 전용 private key와 context-aware KubeVirt connector를
// 기존 제한 SSH 실행기에 조립한다. key Secret이 없으면 오류를 반환해 호출자가 기능만
// 비활성화하고 API 기동은 계속할 수 있게 한다.
func NewKubeVirtFileListRunner(config *rest.Config, privateKeyPath string) (*SSHRunner, error) {
	if privateKeyPath == "" {
		return nil, errors.New("VM file-list private key path is empty")
	}
	privateKey, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read VM file-list private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("parse VM file-list private key: %w", err)
	}
	connector, err := NewKubeVirtConnector(config)
	if err != nil {
		return nil, err
	}

	// VMI는 세션마다 새 host key를 생성해 known_hosts 고정이 어렵다. 대상 VMI의 신뢰 경계는
	// Kubernetes API 인증·인가와 lab-* namespace RoleBinding이 제공한다.
	return NewSSHRunner(connector, signer, ssh.InsecureIgnoreHostKey()) //nolint:gosec
}
