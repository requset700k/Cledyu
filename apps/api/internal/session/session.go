// Package session은 Lab 세션 수명주기의 프로바이더 중립 계약을 정의한다.
// 온프렘 KubeVirt(internal/kubevirt)와 AWS EC2 오버플로우(internal/ec2)가
// 동일한 Provider 인터페이스를 구현하고, 핸들러/디스패처는 이 패키지의 타입에만 의존한다.
package session

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound는 세션을 찾을 수 없을 때 모든 프로바이더가 공통으로 반환한다.
// 핸들러는 errors.Is(err, session.ErrNotFound)로 404 를 판정한다.
var ErrNotFound = errors.New("session not found")

// 프로바이더 식별자 — Session.Provider, 학습 이벤트 VMProvider, vm_provider 응답 필드에 쓰인다.
const (
	ProviderKubeVirt = "kubevirt"
	ProviderEC2      = "ec2"
)

// Session은 프로바이더 무관 세션 상태다. 온프렘/EC2 어느 쪽이 띄웠든 동일 형태로 표현한다.
type Session struct {
	ID     string `json:"id"`
	LabID  string `json:"lab_id"`
	UserID string `json:"user_id"`
	Status string `json:"status"` // provisioning | ready | failed
	// ProvisioningStage는 provisioning 상태를 사용자/운영자가 구분할 수 있게 하는 세부 단계다.
	// 빈 값이면 세부 단계가 없거나 provider가 해당 정보를 제공하지 않는다는 뜻이다.
	ProvisioningStage string    `json:"provisioning_stage,omitempty"`
	StartedAt         time.Time `json:"started_at"`
	ExpiresAt         time.Time `json:"expires_at"`

	// Provider는 이 세션을 프로비저닝한 백엔드다(ProviderKubeVirt | ProviderEC2).
	Provider string `json:"provider"`
	// InstanceID/Region은 EC2 세션에서만 채워진다(검증 요청 라우팅·관측성용).
	InstanceID string `json:"instance_id,omitempty"`
	Region     string `json:"region,omitempty"`
}

// BootInit은 랩별 cloud-init 추가 작업이다(content.InitSpec 에서 변환).
// content 패키지에 대한 역방향 의존을 피하려고 session 이 자체 타입을 가진다.
type BootInit struct {
	Packages []string // cloud-init packages: (apt 설치)
	Runcmd   []string // cloud-init runcmd: 끝에 추가되는 셸 명령
}

// Provider는 Lab 세션 VM의 수명주기를 관리한다. KubeVirt 매니저와 EC2 프로비저너,
// 그리고 둘을 라우팅하는 오버플로우 디스패처가 모두 이 인터페이스를 만족한다.
type Provider interface {
	// Create는 세션 VM 을 프로비저닝하고 provisioning 상태의 Session 을 반환한다.
	Create(ctx context.Context, sessionID, labID, userID string, init BootInit) (*Session, error)
	// Get은 세션의 현재 상태를 반환한다. 없으면 ErrNotFound.
	Get(ctx context.Context, sessionID string) (*Session, error)
	// Delete는 세션 VM 과 부속 리소스를 회수한다. 없으면 ErrNotFound.
	Delete(ctx context.Context, sessionID string) error
	// FindActiveByUser는 user 가 소유한 활성 세션 ID 를 반환한다(없으면 빈 문자열, 에러 아님).
	FindActiveByUser(ctx context.Context, userID string) (string, error)
	// CountActiveSessions는 현재 활성 세션 수를 반환한다(동시 세션 쿼터용).
	CountActiveSessions(ctx context.Context) (int, error)
	// ReapStuckSessions는 timeout 내 ready 가 되지 못한 세션을 회수하고 회수된 ID 목록을 반환한다.
	ReapStuckSessions(ctx context.Context, timeout time.Duration) ([]string, error)
	// ReapExpiredSessions는 TTL(expires_at)이 지난 세션을 회수하고 회수된 ID 목록을 반환한다.
	ReapExpiredSessions(ctx context.Context) ([]string, error)
	// VMIAddress는 세션 VM 에 in-cluster/in-tailnet 으로 도달할 IP 를 반환한다(라이브 터미널/IDE 프록시용).
	// 아직 주소를 못 받았으면 ErrNotFound.
	VMIAddress(ctx context.Context, sessionID string) (string, error)
	// Capacity는 이 프로바이더가 수용 가능한 동시 활성 세션 상한을 반환한다(0이면 무제한).
	// 글로벌 쿼터는 실제로 배선된 프로바이더의 Capacity 합으로 산출해야 한다 — 설정값(KubeVirt+AWS)을
	// 무조건 합산하면 한쪽이 미연결일 때(프로비저너 init 실패 등) 살아있는 프로바이더를 초과 허용한다.
	Capacity() int
}
