// Package checker는 VM에서 체크 항목을 실행하고 결과를 반환한다
package checker

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/requset700k/cledyu/validation-engine/internal/executor"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// 보안을 위한 정규표현식 정의 (Command Injection 방지)
// 허용되는 파일 경로 패턴 — 절대경로만 허용, 셸 메타문자 금지
var safePathRe = regexp.MustCompile(`^/[a-zA-Z0-9/_.\-]+$`)

// 허용되는 프로세스 이름 패턴 — 영문/숫자/하이픈만 허용
var safeNameRe = regexp.MustCompile(`^[a-zA-Z0-9._\-]+$`)

// 허용되는 URL 패턴 — http/https만 허용
var safeURLRe = regexp.MustCompile(`^https?://[a-zA-Z0-9/._:\-]+$`)

// 허용되는 명령어 패턴 — 셸 메타문자(; | & ` $ > < 등) 금지
// %는 stat -c %a 같은 명령에서 사용하므로 허용 — Lab DSL 확정 후 재검토
var safeCommandRe = regexp.MustCompile(`^[a-zA-Z0-9 /._\-=:@%]+$`)

// Run은 체크 항목 하나를 VM에서 실행하고 결과를 돌려준다.
func Run(ctx context.Context, exe executor.VMExecutor, check model.Check) model.CheckResult {
	switch check.Type {
	case model.CheckCommand:
		return runCommand(ctx, exe, check) // 일반 명령어 실행 및 결과 확인
	case model.CheckFileExists:
		return runFileExists(ctx, exe, check) // 파일 존재 여부 확인
	case model.CheckFileContent:
		return runFileContent(ctx, exe, check) // 파일 내용에 특정 문자열 포함 여부 확인
	case model.CheckProcessRunning:
		return runProcessRunning(ctx, exe, check) // 프로세스 실행 여부 확인 (pgrep)
	case model.CheckHTTPResponse:
		return runHTTPResponse(ctx, exe, check) // HTTP 응답 코드 확인 (curl)
	default:
		return model.CheckResult{
			Type:   check.Type,
			Passed: false,
			Detail: fmt.Sprintf("알 수 없는 체크 타입: %s", check.Type),
		}
	}
}

// RunAll은 체크 항목 전체를 실행하고 결과 목록을 돌려준다.
func RunAll(ctx context.Context, exe executor.VMExecutor, checks []model.Check) (results []model.CheckResult, allPassed bool) {
	allPassed = true
	for _, check := range checks {
		result := Run(ctx, exe, check)
		results = append(results, result)
		if !result.Passed {
			allPassed = false
		}
	}
	return results, allPassed
}

// 일반 명령어 실행 검사
func runCommand(ctx context.Context, exe executor.VMExecutor, check model.Check) model.CheckResult {
	if check.Command == "" {
		return fail(check.Type, "cmd가 비어있음")
	}
	if !safeCommandRe.MatchString(check.Command) {
		return fail(check.Type, fmt.Sprintf("허용되지 않는 명령어: %s", check.Command))
	}

	// VM에서 명령어를 실행
	output, err := exe.Exec(ctx, check.Command)
	if err != nil {
		return fail(check.Type, fmt.Sprintf("명령어 실행 실패: %s", err))
	}

	// 기대값(Expect)이 없으면 실행 성공만으로 통과
	if check.Expect == "" {
		return pass(check.Type)
	}

	// 명령어 결과값에 기대하는 문자열이 포함되어 있는지 확인
	if strings.Contains(output, check.Expect) {
		return pass(check.Type)
	}

	return fail(check.Type, fmt.Sprintf("출력에 '%s' 없음", check.Expect))
}

// 파일 존재 여부 검사
func runFileExists(ctx context.Context, exe executor.VMExecutor, check model.Check) model.CheckResult {
	// 허용되지 않는 문자가 포함된 경로 차단 (예: ; rm -rf /) 차단
	if !safePathRe.MatchString(check.Path) {
		return fail(check.Type, fmt.Sprintf("허용되지 않는 경로: %s", check.Path))
	}

	// 쉘의 'test -f' 명령어를 사용하여 파일이 존재하는지 확인
	_, err := exe.Exec(ctx, "test -f "+check.Path)
	if err != nil {
		return fail(check.Type, fmt.Sprintf("파일 없음: %s", check.Path))
	}
	return pass(check.Type)
}

// 파일 내용 검사
func runFileContent(ctx context.Context, exe executor.VMExecutor, check model.Check) model.CheckResult {
	if !safePathRe.MatchString(check.Path) {
		return fail(check.Type, fmt.Sprintf("허용되지 않는 경로: %s", check.Path))
	}

	// 'cat' 명령어로 파일 내용을 읽어온다
	output, err := exe.Exec(ctx, "cat "+check.Path)
	if err != nil {
		return fail(check.Type, fmt.Sprintf("파일 읽기 실패: %s", err))
	}

	// 파일 내용에 기대값이 포함되어 있는지 확인
	if strings.Contains(output, check.Expect) {
		return pass(check.Type)
	}

	return fail(check.Type, fmt.Sprintf("파일에 '%s' 없음", check.Expect))
}

// 프로세스 실행 상태 검사
func runProcessRunning(ctx context.Context, exe executor.VMExecutor, check model.Check) model.CheckResult {
	if !safeNameRe.MatchString(check.Name) {
		return fail(check.Type, fmt.Sprintf("허용되지 않는 프로세스 이름: %s", check.Name))
	}

	// 'pgrep' 명령어로 해당 이름의 프로세스가 실행 중인지 확인
	_, err := exe.Exec(ctx, "pgrep "+check.Name)
	if err != nil {
		return fail(check.Type, fmt.Sprintf("프로세스 실행 중 아님: %s", check.Name))
	}
	return pass(check.Type)
}

// HTTP 응답 상태 검사
func runHTTPResponse(ctx context.Context, exe executor.VMExecutor, check model.Check) model.CheckResult {
	if !safeURLRe.MatchString(check.URL) {
		return fail(check.Type, fmt.Sprintf("허용되지 않는 URL: %s", check.URL))
	}

	// curl 명령어를 사용해 HTTP 응답 코드(예: 200)만 추출
	cmd := fmt.Sprintf("curl -s -o /dev/null -w %%{http_code} %s", check.URL)
	output, err := exe.Exec(ctx, cmd)
	if err != nil {
		return fail(check.Type, fmt.Sprintf("HTTP 요청 실패: %s", err))
	}

	// 실제 응답 코드와 기대값(ExpectCode)을 비교
	expected := fmt.Sprintf("%d", check.ExpectCode)
	if strings.TrimSpace(output) == expected {
		return pass(check.Type)
	}

	return fail(check.Type, fmt.Sprintf("응답 코드 불일치. 기대: %s, 실제: %s", expected, output))
}

// 성공 결과 객체 생성
func pass(t model.CheckType) model.CheckResult {
	return model.CheckResult{Type: t, Passed: true}
}

// 실패 결과 객체 생성
func fail(t model.CheckType, detail string) model.CheckResult {
	return model.CheckResult{Type: t, Passed: false, Detail: detail}
}
