package checker

import (
	"context"
	"errors"
	"testing"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// mockExecutor는 테스트용 가짜 VM executor
type mockExecutor struct {
	output string
	err    error
}

func (m *mockExecutor) Exec(_ context.Context, _ string) (string, error) {
	return m.output, m.err
}

func (m *mockExecutor) Close() {}

func mockOk(output string) *mockExecutor { return &mockExecutor{output: output} }
func mockFail(msg string) *mockExecutor  { return &mockExecutor{err: errors.New(msg)} }

// --- command ---

func TestRunCommand_Pass(t *testing.T) {
	result := Run(context.Background(), mockOk("nginx is running"), model.Check{
		Type:    model.CheckCommand,
		Command: "systemctl status nginx",
		Expect:  "running",
	})
	if !result.Passed {
		t.Errorf("통과해야 하는데 실패: %s", result.Detail)
	}
}

func TestRunCommand_Fail_OutputMismatch(t *testing.T) {
	result := Run(context.Background(), mockOk("stopped"), model.Check{
		Type:    model.CheckCommand,
		Command: "systemctl status nginx",
		Expect:  "running",
	})
	if result.Passed {
		t.Error("출력에 기대값 없으면 실패해야 함")
	}
}

func TestRunCommand_Pass_NoExpect(t *testing.T) {
	result := Run(context.Background(), mockOk("anything"), model.Check{
		Type:    model.CheckCommand,
		Command: "ls /tmp",
	})
	if !result.Passed {
		t.Error("Expect 없으면 실행 성공만으로 통과해야 함")
	}
}

func TestRunCommand_Fail_EmptyCommand(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckCommand,
	})
	if result.Passed {
		t.Error("빈 명령어는 실패해야 함")
	}
}

func TestRunCommand_Fail_Injection(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type:    model.CheckCommand,
		Command: "ls; rm -rf /",
	})
	if result.Passed {
		t.Error("인젝션 시도는 실패해야 함")
	}
}

func TestRunCommand_Fail_ExecError(t *testing.T) {
	result := Run(context.Background(), mockFail("connection refused"), model.Check{
		Type:    model.CheckCommand,
		Command: "ls",
	})
	if result.Passed {
		t.Error("Exec 실패 시 실패해야 함")
	}
}

// --- file_exists ---

func TestRunFileExists_Pass(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckFileExists,
		Path: "/etc/nginx/nginx.conf",
	})
	if !result.Passed {
		t.Errorf("파일 존재 시 통과해야 함: %s", result.Detail)
	}
}

func TestRunFileExists_Fail_NotFound(t *testing.T) {
	result := Run(context.Background(), mockFail("exit 1"), model.Check{
		Type: model.CheckFileExists,
		Path: "/etc/nginx/nginx.conf",
	})
	if result.Passed {
		t.Error("파일 없으면 실패해야 함")
	}
}

func TestRunFileExists_Fail_Injection(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckFileExists,
		Path: "/etc/nginx/nginx.conf; rm -rf /",
	})
	if result.Passed {
		t.Error("인젝션 시도는 실패해야 함")
	}
}

func TestRunFileExists_Fail_RelativePath(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckFileExists,
		Path: "etc/nginx/nginx.conf",
	})
	if result.Passed {
		t.Error("상대경로는 실패해야 함")
	}
}

// --- file_content ---

func TestRunFileContent_Pass(t *testing.T) {
	result := Run(context.Background(), mockOk("127.0.0.1 myapp"), model.Check{
		Type:   model.CheckFileContent,
		Path:   "/etc/hosts",
		Expect: "myapp",
	})
	if !result.Passed {
		t.Errorf("파일에 기대값 있으면 통과해야 함: %s", result.Detail)
	}
}

func TestRunFileContent_Fail_NotContains(t *testing.T) {
	result := Run(context.Background(), mockOk("127.0.0.1 localhost"), model.Check{
		Type:   model.CheckFileContent,
		Path:   "/etc/hosts",
		Expect: "myapp",
	})
	if result.Passed {
		t.Error("파일에 기대값 없으면 실패해야 함")
	}
}

func TestRunFileContent_Fail_Injection(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckFileContent,
		Path: "/etc/hosts && cat /etc/passwd",
	})
	if result.Passed {
		t.Error("인젝션 시도는 실패해야 함")
	}
}

// --- process_running ---

func TestRunProcessRunning_Pass(t *testing.T) {
	result := Run(context.Background(), mockOk("1234"), model.Check{
		Type: model.CheckProcessRunning,
		Name: "nginx",
	})
	if !result.Passed {
		t.Errorf("프로세스 실행 중이면 통과해야 함: %s", result.Detail)
	}
}

func TestRunProcessRunning_Fail_NotRunning(t *testing.T) {
	result := Run(context.Background(), mockFail("exit 1"), model.Check{
		Type: model.CheckProcessRunning,
		Name: "nginx",
	})
	if result.Passed {
		t.Error("프로세스 없으면 실패해야 함")
	}
}

func TestRunProcessRunning_Fail_Injection(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckProcessRunning,
		Name: "nginx; rm -rf /",
	})
	if result.Passed {
		t.Error("인젝션 시도는 실패해야 함")
	}
}

// --- http_response ---

func TestRunHTTPResponse_Pass(t *testing.T) {
	result := Run(context.Background(), mockOk("200"), model.Check{
		Type:       model.CheckHTTPResponse,
		URL:        "http://localhost:80",
		ExpectCode: 200,
	})
	if !result.Passed {
		t.Errorf("응답 코드 일치 시 통과해야 함: %s", result.Detail)
	}
}

func TestRunHTTPResponse_Fail_CodeMismatch(t *testing.T) {
	result := Run(context.Background(), mockOk("404"), model.Check{
		Type:       model.CheckHTTPResponse,
		URL:        "http://localhost:80",
		ExpectCode: 200,
	})
	if result.Passed {
		t.Error("응답 코드 불일치 시 실패해야 함")
	}
}

func TestRunHTTPResponse_Fail_Injection(t *testing.T) {
	result := Run(context.Background(), mockOk("200"), model.Check{
		Type:       model.CheckHTTPResponse,
		URL:        "http://localhost:80; rm -rf /",
		ExpectCode: 200,
	})
	if result.Passed {
		t.Error("인젝션 시도는 실패해야 함")
	}
}

func TestRunHTTPResponse_Fail_NoHTTPS(t *testing.T) {
	result := Run(context.Background(), mockOk("200"), model.Check{
		Type:       model.CheckHTTPResponse,
		URL:        "ftp://localhost:80",
		ExpectCode: 200,
	})
	if result.Passed {
		t.Error("http/https 외 프로토콜은 실패해야 함")
	}
}

// --- RunAll ---

func TestRunAll_AllPass(t *testing.T) {
	checks := []model.Check{
		{Type: model.CheckCommand, Command: "ls", Expect: "file"},
		{Type: model.CheckProcessRunning, Name: "nginx"},
	}
	exe := mockOk("file\nnginx")
	_, allPassed := RunAll(context.Background(), exe, checks)
	if !allPassed {
		t.Error("전부 통과 시 allPassed가 true여야 함")
	}
}

func TestRunAll_OneFail(t *testing.T) {
	checks := []model.Check{
		{Type: model.CheckCommand, Command: "ls", Expect: "file"},
		{Type: model.CheckCommand, Command: "ls", Expect: "없는값"},
	}
	results, allPassed := RunAll(context.Background(), mockOk("file"), checks)
	if allPassed {
		t.Error("하나라도 실패 시 allPassed가 false여야 함")
	}
	if len(results) != 2 {
		t.Errorf("결과 개수가 2여야 하는데 %d", len(results))
	}
}

func TestRunAll_UnknownType(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckType("unknown"),
	})
	if result.Passed {
		t.Error("알 수 없는 타입은 실패해야 함")
	}
}
