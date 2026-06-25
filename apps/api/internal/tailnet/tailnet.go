// Package tailnet 은 api 파드를 tsnet 으로 tailnet 에 노드로 가입시켜, EC2 오버플로우 세션
// (tailnet MagicDNS 로만 도달)에 라이브 터미널 SSH 를 붙일 수 있게 한다. 클러스터 파드는
// tailnet/MagicDNS 에 직접 못 닿으므로, api 자신이 tsnet 노드가 되어 그 다이얼러로 연결한다.
package tailnet

import (
	"context"
	"fmt"
	"net"

	"go.uber.org/zap"
	"tailscale.com/tsnet"
)

// Config 는 api tailnet 노드 설정이다.
type Config struct {
	Hostname string // tailnet 노드 호스트명(예: cledyu-api). 비면 tsnet 이 OS 호스트명(파드명) 사용.
	AuthKey  string // tag:cledyu-api reusable+ephemeral authkey.
	StateDir string // tsnet 상태 디렉터리. 비면 tsnet 기본 경로.
}

// Node 는 tailnet 에 가입한 api 노드다. Dial 로 tailnet 대상에 TCP 연결한다.
type Node struct {
	srv *tsnet.Server
}

// New 는 tsnet 서버를 시작하고 tailnet 가입이 끝날 때까지(ctx 상한) 기다린다.
// Ephemeral 노드라 프로세스 종료 시 tailnet 에서 자동 정리된다.
func New(ctx context.Context, cfg Config, log *zap.Logger) (*Node, error) {
	if cfg.AuthKey == "" {
		return nil, fmt.Errorf("tailnet: authkey 가 비어 있음")
	}
	srv := &tsnet.Server{
		Hostname:  cfg.Hostname,
		AuthKey:   cfg.AuthKey,
		Ephemeral: true,
		Dir:       cfg.StateDir,
		Logf:      func(format string, args ...any) { log.Debug("tsnet: " + fmt.Sprintf(format, args...)) },
	}
	if _, err := srv.Up(ctx); err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("tailnet: 노드 기동 실패: %w", err)
	}
	return &Node{srv: srv}, nil
}

// Dial 은 tailnet 의 addr(MagicDNS 호스트명:포트)로 TCP 연결한다. ec2.DialFunc 와 시그니처 호환이라
// 라이브 터미널/IDE 핸들러에 그대로 주입된다.
func (n *Node) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.srv.Dial(ctx, network, addr)
}

// Close 는 tailnet 노드를 종료한다(ephemeral 이므로 tailnet 노드 목록에서도 제거된다).
func (n *Node) Close() error {
	return n.srv.Close()
}
