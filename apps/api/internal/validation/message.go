// Package validation contains the Session API side of the validation-engine contract.
package validation

import "context"

type Publisher interface {
	PublishRequest(ctx context.Context, req ValidationRequest) error
}

type VMType string

const (
	VMTypeKubeVirt VMType = "kubevirt"
	VMTypeEC2      VMType = "ec2"
)

type CheckType string

const (
	CheckCommand        CheckType = "command"
	CheckFileExists     CheckType = "file_exists"
	CheckFileContent    CheckType = "file_content"
	CheckProcessRunning CheckType = "process_running"
	CheckHTTPResponse   CheckType = "http_response"
)

type VMSpec struct {
	Type       VMType `json:"type"`
	Name       string `json:"name,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	Region     string `json:"region,omitempty"`
}

type Check struct {
	Type       CheckType `json:"type"`
	Command    string    `json:"cmd,omitempty"`
	Path       string    `json:"path,omitempty"`
	URL        string    `json:"url,omitempty"`
	Name       string    `json:"name,omitempty"`
	Expect     string    `json:"expect,omitempty"`
	ExpectCode int       `json:"expect_code,omitempty"`
}

type ValidationRequest struct {
	TraceID   string  `json:"trace_id,omitempty"`
	SessionID string  `json:"session_id"`
	StepID    int     `json:"step_id"`
	VM        VMSpec  `json:"vm"`
	Checks    []Check `json:"checks"`
}
