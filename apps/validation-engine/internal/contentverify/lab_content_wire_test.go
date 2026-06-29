// Package contentverify는 실제 랩 콘텐츠(apps/api/internal/content/labs/*.yaml)를
// 검증 엔진(checker/model)에 그대로 흘려보내, 정답 상태는 통과·오답 상태는 실패로
// 올바르게 판별하는지 확인하는 회귀 테스트다.
//
// 무엇을 막아주나:
//   - Session API 가 Kafka(validation-requests)로 발행하는 Check 의 JSON 표현과
//     검증 엔진이 소비하는 model.Check 의 JSON 표현이 어긋나면, 직렬화 과정에서
//     필드가 조용히 유실되어 체크가 깨진다(예: command 가 cmd 로 정렬되지 않으면
//     command 타입 체크 전부 실패). 이 테스트가 그 드리프트를 컴파일/CI 단계에서 잡는다.
//
// 왜 여기(validation-engine 모듈) 안에 있나:
//   - apps/api 와 apps/validation-engine 은 별도 Go 모듈이고, checker/model 은
//     internal/ 아래라 다른 모듈에서 import 할 수 없다. 그래서 랩 YAML 을
//     상대경로로 직접 읽어 엔진의 실제 판별 로직(checker.Run)에 통과시킨다.
package contentverify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/checker"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
	"gopkg.in/yaml.v3"
)

// wireCheck 는 Session API 가 Kafka(validation-requests)로 발행하는 Check 의
// 와이어 표현을 재현한다.
//
//   - yaml 태그 : 랩 DSL 키(랩 저자가 작성하는 필드명)와 일치한다.
//   - json 태그 : Session API 가 실제로 내보내는 바이트(message.Check)와 동일해야 한다.
//     이 테스트의 핵심 목적이 "API 가 실제로 내보내는 바이트"를 그대로 재현하는 것이기 때문이다.
type wireCheck struct {
	Type string `yaml:"type" json:"type"`
	// 엔진 model.Check 의 cmd 와 정렬하려고 json:"cmd" 를 쓴다. 이 둘이 어긋나면
	// 와이어 직렬화 후 Command 가 유실되며, 아래 TestLabContent_WirePreservesFields 가 잡는다.
	Command    string `yaml:"command,omitempty" json:"cmd,omitempty"`
	Path       string `yaml:"path,omitempty" json:"path,omitempty"`
	URL        string `yaml:"url,omitempty" json:"url,omitempty"`
	Name       string `yaml:"name,omitempty" json:"name,omitempty"`
	Expect     string `yaml:"expect,omitempty" json:"expect,omitempty"`
	ExpectCode int    `yaml:"expect_code,omitempty" json:"expect_code,omitempty"`
	// Timeout 도 API 발행 Check(message.Check json:"timeout")의 일부다. 빠지면 미러가
	// 어긋나 per-check timeout 의 와이어 유실을 못 잡는다.
	Timeout int `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// wireLab 는 랩 YAML 에서 검증에 필요한 부분(id·steps·checks)만 추린 형태다.
type wireLab struct {
	ID    string `yaml:"id"`
	Steps []struct {
		ID     int         `yaml:"id"`
		Title  string      `yaml:"title"`
		Checks []wireCheck `yaml:"checks"`
	} `yaml:"steps"`
}

// labsDir 는 이 테스트 파일 위치를 기준으로 apps/api/internal/content/labs 경로를 구한다.
// runtime.Caller 를 쓰므로 테스트 실행 디렉터리(CWD)와 무관하다.
func labsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 로 테스트 파일 경로를 구하지 못함")
	}
	// thisFile = .../apps/validation-engine/internal/contentverify/lab_content_wire_test.go
	// 목표      = .../apps/api/internal/content/labs
	dir := filepath.Dir(thisFile)
	return filepath.Join(dir, "..", "..", "..", "api", "internal", "content", "labs")
}

// loadLabs 는 실제 랩 YAML 을 모두 읽어 파싱한다.
func loadLabs(t *testing.T) []wireLab {
	t.Helper()
	dir := labsDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("랩 디렉터리 읽기 실패 %s: %v", dir, err)
	}
	var labs []wireLab
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("랩 파일 읽기 실패 %s: %v", e.Name(), err)
		}
		var lab wireLab
		if err := yaml.Unmarshal(data, &lab); err != nil {
			t.Fatalf("랩 파싱 실패 %s: %v", e.Name(), err)
		}
		labs = append(labs, lab)
	}
	if len(labs) == 0 {
		t.Fatalf("랩을 하나도 로드하지 못함 (dir=%s)", dir)
	}
	return labs
}

// toModelCheck 는 API 발행→엔진 소비의 실제 와이어 경로를 재현한다:
// wireCheck(JSON marshal) → Kafka → model.Check(JSON unmarshal).
func toModelCheck(t *testing.T, wc wireCheck) model.Check {
	t.Helper()
	b, err := json.Marshal(wc) // Session API 가 발행하는 바이트
	if err != nil {
		t.Fatalf("wireCheck marshal 실패: %v", err)
	}
	var mc model.Check
	if err := json.Unmarshal(b, &mc); err != nil { // Validation Engine 이 소비하는 바이트
		t.Fatalf("model.Check unmarshal 실패: %v", err)
	}
	return mc
}

// fakeExecutor 는 출력/에러를 미리 정해두는 테스트용 VM executor 다(VMExecutor 인터페이스 충족).
type fakeExecutor struct {
	output string
	err    error
}

func (f fakeExecutor) Exec(context.Context, string) (string, error) { return f.output, f.err }
func (f fakeExecutor) DefaultTimeout() time.Duration                { return 20 * time.Second }
func (f fakeExecutor) Close()                                       {}

// satisfying 은 해당 체크를 "정답 상태"로 만드는 VM 출력을 돌려준다(체크 타입에서 파생).
func satisfying(mc model.Check) fakeExecutor {
	switch mc.Type {
	case model.CheckFileExists, model.CheckDirExists, model.CheckFileAbsent, model.CheckProcessRunning:
		return fakeExecutor{output: "ok"} // test -f / test -d / test ! -f / pgrep 성공(에러 없음)
	case model.CheckFileContent, model.CheckCommand:
		return fakeExecutor{output: "··· " + mc.Expect + " ···"} // 출력/내용에 Expect 포함
	case model.CheckFileContentAbsent:
		return fakeExecutor{output: "··· 무관한 내용 ···"} // 정답=Expect 가 내용에 없음
	case model.CheckHTTPResponse:
		return fakeExecutor{output: strconv.Itoa(mc.ExpectCode)}
	default:
		return fakeExecutor{output: ""}
	}
}

// violating 은 해당 체크를 "오답 상태"로 만드는 VM 출력을 돌려준다.
func violating(mc model.Check) fakeExecutor {
	switch mc.Type {
	case model.CheckFileExists, model.CheckDirExists, model.CheckFileAbsent, model.CheckProcessRunning:
		return fakeExecutor{err: errors.New("exit status 1")} // 파일 없음 / 디렉터리 없음 / (file_absent는)파일 있음 / 프로세스 없음
	case model.CheckFileContent, model.CheckCommand:
		return fakeExecutor{output: ""} // 빈 출력 — 비어있지 않은 Expect 를 절대 포함하지 않음
	case model.CheckFileContentAbsent:
		return fakeExecutor{output: "··· " + mc.Expect + " ···"} // 오답=있으면 안 되는 Expect 가 내용에 있음
	case model.CheckHTTPResponse:
		return fakeExecutor{output: strconv.Itoa(mc.ExpectCode + 1)} // 기대와 다른 코드
	default:
		return fakeExecutor{output: ""}
	}
}

// TestLabContent_WirePreservesFields 는 랩의 각 체크가 API→엔진 와이어 직렬화를 거친 뒤에도
// 판별에 필요한 필드를 잃지 않는지 검증한다. command/cmd 같은 태그 드리프트를 잡는다.
func TestLabContent_WirePreservesFields(t *testing.T) {
	for _, lab := range loadLabs(t) {
		for _, step := range lab.Steps {
			for i, wc := range step.Checks {
				mc := toModelCheck(t, wc)
				where := func() string {
					return lab.ID + " step " + strconv.Itoa(step.ID) + " check " + strconv.Itoa(i) + " (" + wc.Type + ")"
				}
				// per-check timeout 은 모든 타입에 공통이다. YAML 에 적은 값이
				// 와이어 직렬화 후에도 그대로 보존돼야 한다(timeout 태그 드리프트 차단).
				if wc.Timeout != mc.Timeout {
					t.Errorf("%s: timeout 이 와이어 직렬화 후 %d→%d 로 바뀜 — API/엔진 JSON 태그 불일치", where(), wc.Timeout, mc.Timeout)
				}
				switch model.CheckType(wc.Type) {
				case model.CheckCommand:
					if mc.Command == "" {
						t.Errorf("%s: command 가 와이어 직렬화 후 비어버림 — API 의 발행 Check 와 엔진 model.Check 의 JSON 태그 불일치(command vs cmd)", where())
					}
					if mc.Expect == "" {
						t.Errorf("%s: expect 가 비어있음", where())
					}
				case model.CheckFileExists, model.CheckDirExists, model.CheckFileAbsent:
					if mc.Path == "" {
						t.Errorf("%s: path 가 비어있음", where())
					}
				case model.CheckFileContent, model.CheckFileContentAbsent:
					if mc.Path == "" || mc.Expect == "" {
						t.Errorf("%s: path/expect 가 비어있음", where())
					}
				case model.CheckProcessRunning:
					if mc.Name == "" {
						t.Errorf("%s: name 이 비어있음", where())
					}
				case model.CheckHTTPResponse:
					if mc.URL == "" || mc.ExpectCode == 0 {
						t.Errorf("%s: url/expect_code 가 비어있음", where())
					}
				default:
					t.Errorf("%s: 알 수 없는 체크 타입", where())
				}
			}
		}
	}
}

// TestLabContent_JudgesCorrectly 는 실제 랩의 모든 체크에 대해, 정답 상태면 통과하고
// 오답 상태면 실패하는지를 엔진의 실제 판별 로직(checker.Run)으로 검증한다.
func TestLabContent_JudgesCorrectly(t *testing.T) {
	ctx := context.Background()
	for _, lab := range loadLabs(t) {
		for _, step := range lab.Steps {
			for i, wc := range step.Checks {
				mc := toModelCheck(t, wc)
				where := lab.ID + " step " + strconv.Itoa(step.ID) + " check " + strconv.Itoa(i) + " (" + wc.Type + ")"

				if res := checker.Run(ctx, satisfying(mc), mc); !res.Passed {
					t.Errorf("%s: 정답 상태인데 실패로 판별됨: %s", where, res.Detail)
				}
				if res := checker.Run(ctx, violating(mc), mc); res.Passed {
					t.Errorf("%s: 오답 상태인데 통과로 판별됨(오탐)", where)
				}
			}
		}
	}
}

// TestLabContent_StepFailsIfAnyCheckWrong 는 스텝 단위에서, 모든 체크가 정답이면
// 통과하고 한 체크라도 오답이면 스텝이 실패하는지(AND 판정) 검증한다.
func TestLabContent_StepFailsIfAnyCheckWrong(t *testing.T) {
	ctx := context.Background()
	for _, lab := range loadLabs(t) {
		for _, step := range lab.Steps {
			if len(step.Checks) == 0 {
				continue
			}
			checks := make([]model.Check, len(step.Checks))
			for i, wc := range step.Checks {
				checks[i] = toModelCheck(t, wc)
			}
			where := lab.ID + " step " + strconv.Itoa(step.ID)

			// 모든 체크 정답 → 스텝 통과. 각 체크를 만족하는 출력은 서로 달라
			// 단일 출력 executor 로는 동시에 만족시킬 수 없으므로 체크별로 개별 평가한다.
			allPass := true
			for _, mc := range checks {
				if res := checker.Run(ctx, satisfying(mc), mc); !res.Passed {
					allPass = false
				}
			}
			if !allPass {
				t.Errorf("%s: 모든 체크가 정답 상태인데 스텝이 통과하지 못함", where)
			}

			// 첫 체크만 오답 → 스텝 실패.
			results := make([]model.CheckResult, len(checks))
			for i, mc := range checks {
				exe := satisfying(mc)
				if i == 0 {
					exe = violating(mc)
				}
				results[i] = checker.Run(ctx, exe, mc)
			}
			stepPassed := true
			for _, r := range results {
				if !r.Passed {
					stepPassed = false
				}
			}
			if stepPassed {
				t.Errorf("%s: 한 체크가 오답인데 스텝이 통과로 판별됨", where)
			}
		}
	}
}
