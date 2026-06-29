// Package model은 Kafka 토픽에서 주고받는 메시지 구조를 정의한다.
// validation-requests  : Session API → Validation Engine
// validation-results   : Validation Engine → Session API
package model

// VMType은 VM이 온프렘 KubeVirt인지 AWS EC2인지 구분
type VMType string

const (
	VMTypeKubeVirt VMType = "kubevirt"
	VMTypeEC2      VMType = "ec2"
)

// CheckType은 검증 항목의 종류다.
type CheckType string

// 추가 타입이 필요하면 message.go와 checker.go를 같이 확장
const (
	// CheckCommand: VM에서 명령을 실행하고 출력에 특정 문자열이 있는지 확인
	// 예) kubectl get pods 실행 → "nginx" 포함 여부
	CheckCommand CheckType = "command"

	// CheckFileExists: 파일이 존재하는지 확인
	// 예) /etc/nginx/nginx.conf 가 있는지
	CheckFileExists CheckType = "file_exists"

	// CheckDirExists: 디렉터리가 존재하는지 확인
	// 예) /home/lab/work/scripts 디렉터리가 있는지
	// file_exists(test -f)는 디렉터리를 인정하지 않으므로 디렉터리 검증은 이 타입을 쓴다.
	CheckDirExists CheckType = "dir_exists"

	// CheckFileAbsent: 파일이 존재하지 "않는지" 확인 (test ! -f)
	// 예) ~/work/backup/debug.txt 가 복사되지 않았는지 같은 부재 조건
	CheckFileAbsent CheckType = "file_absent"

	// CheckFileContentAbsent: 파일 내용에 특정 문자열이 "없는지" 확인
	// 예) grep 으로 필터링한 결과에 nologin 행이 섞이지 않았는지 같은 내용 부재 조건
	CheckFileContentAbsent CheckType = "file_content_absent"

	// CheckFileContent: 파일 내용에 특정 문자열이 있는지 확인
	// 예) /etc/hosts 에 "myapp" 라인이 있는지
	CheckFileContent CheckType = "file_content"

	// CheckProcessRunning: 프로세스가 실행 중인지 확인
	// 예) nginx 프로세스가 떠 있는지
	CheckProcessRunning CheckType = "process_running"

	// CheckHTTPResponse: VM 안에서 HTTP 요청을 보내고 응답 코드가 맞는지 확인
	// 예) localhost:80 에 curl 날려서 200이 오는지
	CheckHTTPResponse CheckType = "http_response"

	// CheckRequestError: 요청 자체가 유효하지 않을 때 사용하는 타입
	// session_id 없음, checks 비어있음, VM 스펙 오류 등 — VM 실행과 무관한 요청 레벨 오류
	CheckRequestError CheckType = "request_error"
)

// VMSpec은 접속할 VM의 정보
// Type에 따라 사용하는 필드가 달라진다
type VMSpec struct {
	// Type: "kubevirt" 또는 "ec2"
	Type VMType `json:"type"`

	// KubeVirt VM일 때만 사용
	Name      string `json:"name,omitempty"`      // VM 이름 (예: "lab-vm-abc123")
	Namespace string `json:"namespace,omitempty"` // K8s 네임스페이스 (예: "lab-sessions")

	// EC2 VM일 때만 사용
	InstanceID string `json:"instance_id,omitempty"` // EC2 인스턴스 ID (예: "i-0abc1234567890")
	Region     string `json:"region,omitempty"`      // AWS 리전 (예: "ap-northeast-2")
}

// Check는 검증 항목 하나
// Type에 따라 사용하는 필드가 달라진다
type Check struct {
	// Type: 어떤 종류의 검증인지
	Type CheckType `json:"type"`

	// command, file_content, http_response 에서 사용
	Command string `json:"cmd,omitempty"`  // 실행할 명령어
	Path    string `json:"path,omitempty"` // 파일 경로
	URL     string `json:"url,omitempty"`  // HTTP URL
	Name    string `json:"name,omitempty"` // 프로세스 이름

	// 기대값
	Expect     string `json:"expect,omitempty"`      // 출력/내용에 포함돼야 할 문자열
	ExpectCode int    `json:"expect_code,omitempty"` // 기대하는 HTTP 상태코드

	// Timeout: 이 체크 하나의 실행 제한 시간(초). 0이면 executor별 기본값(KubeVirt 20s / EC2 5m).
	// ansible-playbook 처럼 기본 20s를 넘기는 명령에 더 큰 값을 준다.
	Timeout int `json:"timeout,omitempty"`
}

// ValidationRequest는 validation-requests 토픽에 들어가는 메시지
// Session API가 발행하고, Validation Engine이 소비
type ValidationRequest struct {
	// TraceID: 요청 하나의 전체 흐름을 추적하는 ID — Session API가 생성해서 넣어준다
	// 없으면 빈 문자열로 처리하며 session_id + step_id로 추적 가능
	TraceID string `json:"trace_id,omitempty"`

	// SessionID: 어느 수강생 세션인지 (예: "sess-abc123")
	SessionID string `json:"session_id"`

	// StepID: 몇 번째 단계를 검증하는지 (예: 2)
	StepID int `json:"step_id"`

	// VM: 접속할 VM 정보
	VM VMSpec `json:"vm"`

	// Checks: 확인할 항목 목록. 모두 통과해야 해당 단계가 passed
	Checks []Check `json:"checks"`
}

// CheckResult는 검증 항목 하나의 결과다.
type CheckResult struct {
	Type   CheckType `json:"type"`
	Passed bool      `json:"passed"`
	Detail string    `json:"detail,omitempty"` // 실패 이유 또는 실행 결과 요약
}

// ValidationResult는 validation-results 토픽에 들어가는 메시지
// Validation Engine이 발행하고, Session API가 소비
type ValidationResult struct {
	// TraceID: 요청의 trace_id를 그대로 넘겨 Session API가 추적할 수 있게 한다
	TraceID string `json:"trace_id,omitempty"`

	// SessionID, StepID: 요청과 동일하게 그대로 넘긴다
	SessionID string `json:"session_id"`
	StepID    int    `json:"step_id"`

	// Passed: Checks 항목이 모두 통과하면 true
	Passed bool `json:"passed"`

	// Checks: 항목별 세부 결과
	Checks []CheckResult `json:"checks"`

	// DurationMS: 검증에 걸린 시간 (밀리초)
	DurationMS int64 `json:"duration_ms"`
}
