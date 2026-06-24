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
	CheckCommand        CheckType = "command"
	CheckFileExists     CheckType = "file_exists"
	CheckDirExists      CheckType = "dir_exists"
	CheckFileContent    CheckType = "file_content"
	CheckProcessRunning CheckType = "process_running"
	CheckHTTPResponse   CheckType = "http_response"
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
}

// ValidationRequest is published to the validation-requests Kafka topic.
type ValidationRequest struct {
	TraceID   string  `json:"trace_id,omitempty"`
	SessionID string  `json:"session_id"`
	StepID    int     `json:"step_id"`
	VM        VMSpec  `json:"vm"`
	Checks    []Check `json:"checks"`
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
