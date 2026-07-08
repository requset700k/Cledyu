// Package validation contains the Session API side of the validation-engine contract.
package validation

import "context"

// Publisher sends validation requests to validation-engine.
type Publisher interface {
	PublishRequest(ctx context.Context, req ValidationRequest) error
}

// VMType identifies the VM provider that validation-engine should use.
type VMType string

const (
	VMTypeKubeVirt VMType = "kubevirt"
	VMTypeEC2      VMType = "ec2"
)

// CheckType is aligned with apps/validation-engine/internal/model.CheckType.
type CheckType string

const (
	CheckCommand           CheckType = "command"
	CheckFileExists        CheckType = "file_exists"
	CheckDirExists         CheckType = "dir_exists"
	CheckFileAbsent        CheckType = "file_absent"
	CheckFileContent       CheckType = "file_content"
	CheckFileContentAbsent CheckType = "file_content_absent"
	CheckProcessRunning    CheckType = "process_running"
	CheckHTTPResponse      CheckType = "http_response"
)

// VMSpec tells validation-engine which lab VM to inspect.
type VMSpec struct {
	Type       VMType `json:"type"`
	Name       string `json:"name,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	Region     string `json:"region,omitempty"`
}

// Check is one validation assertion.
type Check struct {
	Type       CheckType `json:"type"`
	Command    string    `json:"cmd,omitempty"`
	Path       string    `json:"path,omitempty"`
	URL        string    `json:"url,omitempty"`
	Name       string    `json:"name,omitempty"`
	Expect     string    `json:"expect,omitempty"`
	ExpectCode int       `json:"expect_code,omitempty"`
	Timeout    int       `json:"timeout,omitempty"` // 체크 실행 제한 시간(초). 0이면 executor별 기본값(KubeVirt 20s / EC2 5m).
}

// ValidationRequest is published to the validation-requests Kafka topic.
type ValidationRequest struct {
	// TraceID는 요청별 고유 ID다(Redis 시작시간 키·결과 상관관계용). 하나의 W3C trace로 여러 검증을
	// 묶어도 요청마다 달라야 하므로 OTel trace ID가 아니라 요청별 랜덤값을 쓴다.
	TraceID string `json:"trace_id,omitempty"`
	// Traceparent는 이 요청의 W3C trace context(00-traceID-spanID-flags)다. validation-engine이 이를
	// 이어받아 자신의 span을 같은 분산 trace의 자식으로 만들면, 운영자가 결과에서 Tempo trace로
	// 직접 이동할 수 있다(TraceID는 상관관계용 고유 ID로 유지).
	Traceparent string  `json:"traceparent,omitempty"`
	SessionID   string  `json:"session_id"`
	StepID      int     `json:"step_id"`
	VM          VMSpec  `json:"vm"`
	Checks      []Check `json:"checks"`
}

// CheckResult is one check's outcome (aligned with validation-engine model.CheckResult).
type CheckResult struct {
	Type   CheckType `json:"type"`
	Passed bool      `json:"passed"`
	Detail string    `json:"detail,omitempty"` // 실패 이유 또는 실행 결과 요약
}

// ValidationResult is consumed from the validation-results Kafka topic
// (aligned with validation-engine model.ValidationResult).
type ValidationResult struct {
	TraceID    string        `json:"trace_id,omitempty"`
	SessionID  string        `json:"session_id"`
	StepID     int           `json:"step_id"`
	Passed     bool          `json:"passed"` // Checks 가 모두 통과하면 true
	Checks     []CheckResult `json:"checks"`
	DurationMS int64         `json:"duration_ms"`
}
