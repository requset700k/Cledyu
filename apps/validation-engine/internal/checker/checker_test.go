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

func TestRunCommand_Fail_NoExpect(t *testing.T) {
	result := Run(context.Background(), mockOk("anything"), model.Check{
		Type:    model.CheckCommand,
		Command: "ls /tmp",
	})
	if result.Passed {
		t.Error("Expect 없으면 실패해야 함")
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

// --- dir_exists ---

func TestRunDirExists_Pass(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckDirExists,
		Path: "/home/lab/work/scripts",
	})
	if !result.Passed {
		t.Errorf("디렉터리 존재 시 통과해야 함: %s", result.Detail)
	}
}

func TestRunDirExists_Fail_NotFound(t *testing.T) {
	result := Run(context.Background(), mockFail("exit 1"), model.Check{
		Type: model.CheckDirExists,
		Path: "/home/lab/work/scripts",
	})
	if result.Passed {
		t.Error("디렉터리 없으면 실패해야 함")
	}
}

func TestRunDirExists_Fail_Injection(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckDirExists,
		Path: "/home/lab/work/scripts; rm -rf /",
	})
	if result.Passed {
		t.Error("인젝션 시도는 실패해야 함")
	}
}

func TestRunDirExists_Fail_RelativePath(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckDirExists,
		Path: "home/lab/work/scripts",
	})
	if result.Passed {
		t.Error("상대경로는 실패해야 함")
	}
}

// --- file_absent ---

func TestRunFileAbsent_Pass(t *testing.T) {
	// test ! -f 가 성공(파일 없음) → 통과
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckFileAbsent,
		Path: "/home/lab/work/backup/debug.txt",
	})
	if !result.Passed {
		t.Errorf("파일이 없으면 통과해야 함: %s", result.Detail)
	}
}

func TestRunFileAbsent_Fail_Present(t *testing.T) {
	// test ! -f 가 실패(파일 있음) → 실패
	result := Run(context.Background(), mockFail("exit 1"), model.Check{
		Type: model.CheckFileAbsent,
		Path: "/home/lab/work/backup/debug.txt",
	})
	if result.Passed {
		t.Error("존재하면 안 되는 파일이 있으면 실패해야 함")
	}
}

func TestRunFileAbsent_Fail_Injection(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckFileAbsent,
		Path: "/home/lab/work/backup/debug.txt; rm -rf /",
	})
	if result.Passed {
		t.Error("인젝션 시도는 실패해야 함")
	}
}

func TestRunFileAbsent_Fail_RelativePath(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type: model.CheckFileAbsent,
		Path: "home/lab/work/backup/debug.txt",
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

// --- file_content_absent ---

func TestRunFileContentAbsent_Pass(t *testing.T) {
	// 기대 문자열이 파일에 없음 → 통과
	result := Run(context.Background(), mockOk("root:/bin/bash\nlab:/bin/bash"), model.Check{
		Type:   model.CheckFileContentAbsent,
		Path:   "/home/lab/work/bash-users.txt",
		Expect: "nologin",
	})
	if !result.Passed {
		t.Errorf("기대 문자열이 없으면 통과해야 함: %s", result.Detail)
	}
}

func TestRunFileContentAbsent_Fail_Present(t *testing.T) {
	// 기대 문자열이 파일에 있음(필터링 안 함) → 실패
	result := Run(context.Background(), mockOk("daemon:/usr/sbin/nologin\nlab:/bin/bash"), model.Check{
		Type:   model.CheckFileContentAbsent,
		Path:   "/home/lab/work/bash-users.txt",
		Expect: "nologin",
	})
	if result.Passed {
		t.Error("있으면 안 되는 문자열이 있으면 실패해야 함")
	}
}

func TestRunFileContentAbsent_Pass_FileMissing(t *testing.T) {
	// 파일이 아예 없으면 찾을 내용도 없으므로 '내용 부재'는 공허하게 충족 → 통과(vacuous pass).
	// 단독 사용 랩에서 파일 미생성을 '파일 읽기 실패'라는 혼동되는 사유로 떨어뜨리지 않기 위함.
	result := Run(context.Background(), mockFail("test: no such file"), model.Check{
		Type:   model.CheckFileContentAbsent,
		Path:   "/home/lab/work/bash-users.txt",
		Expect: "nologin",
	})
	if !result.Passed {
		t.Errorf("파일이 없으면 vacuous pass 해야 함: %s", result.Detail)
	}
}

func TestRunFileContentAbsent_Fail_EmptyExpect(t *testing.T) {
	// expect 비어있으면 항상 "있음"으로 판정돼 통과 불가 → 실패
	result := Run(context.Background(), mockOk("anything"), model.Check{
		Type:   model.CheckFileContentAbsent,
		Path:   "/home/lab/work/bash-users.txt",
		Expect: "",
	})
	if result.Passed {
		t.Error("expect가 비어있으면 실패해야 함")
	}
}

func TestRunFileContentAbsent_Fail_Injection(t *testing.T) {
	result := Run(context.Background(), mockOk(""), model.Check{
		Type:   model.CheckFileContentAbsent,
		Path:   "/home/lab/work/bash-users.txt && cat /etc/passwd",
		Expect: "nologin",
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
