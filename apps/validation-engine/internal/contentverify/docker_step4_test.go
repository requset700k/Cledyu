package contentverify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/checker"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// docker4Fake는 step4 명령별로 다른 결과를 준다.
//   - "docker inspect ..." → 컨테이너 JSON(State.Status, Config.Image) / 없으면 NotFound 에러
//   - "docker port ..."    → 80 포트의 호스트 매핑(미매핑이면 빈 출력)
//   - "curl ..."           → 응답 본문
type docker4Fake struct {
	inspectOut string
	inspectErr error
	portOut    string
	curlOut    string
}

func (d docker4Fake) Exec(_ context.Context, cmd string) (string, error) {
	switch {
	case strings.Contains(cmd, "docker inspect"):
		return d.inspectOut, d.inspectErr
	case strings.Contains(cmd, "docker port"):
		return d.portOut, nil
	case strings.Contains(cmd, "curl"):
		return d.curlOut, nil
	default:
		return "", nil
	}
}
func (d docker4Fake) DefaultTimeout() time.Duration { return 20 * time.Second }
func (d docker4Fake) Close()                        {}

func dockerStep4Checks(t *testing.T) []model.Check {
	t.Helper()
	var out []model.Check
	for _, lab := range loadLabs(t) {
		if lab.ID != "lab-docker-basics" {
			continue
		}
		for _, s := range lab.Steps {
			if s.ID != 4 {
				continue
			}
			for _, wc := range s.Checks {
				out = append(out, toModelCheck(t, wc))
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("lab-docker-basics step4 체크를 찾지 못함")
	}
	return out
}

// step4 지문은 "myweb:v1 이미지를 web2 이름으로 8081 에 실행"을 명시한다. 결과(curl→built with
// dockerfile)만 보면 이름/이미지/포트를 다르게 해도 통과한다(예: 다른 이름, 다른 이미지에 같은
// 내용). 지문대로 만들었는지 — web2(이름)·myweb:v1(이미지)·8081(포트) — 를 함께 검증해야 한다.
func TestDockerStep4_VerifiesContainerSpec(t *testing.T) {
	step4 := dockerStep4Checks(t)
	runningMyweb := `[{"State":{"Status":"running"},"Config":{"Image":"myweb:v1"}}]`

	// 정답: web2(running) + myweb:v1 + 8081 + built with dockerfile
	good := docker4Fake{inspectOut: runningMyweb, portOut: "0.0.0.0:8081", curlOut: "built with dockerfile"}
	if _, ok := checker.RunAll(context.Background(), good, step4); !ok {
		t.Error("web2 + myweb:v1 + 8081 + 내용 정답은 step4를 통과해야 함")
	}

	// 이름이 web2 가 아님 → docker inspect web2 NotFound → 탈락.
	wrongName := docker4Fake{inspectErr: errors.New("Error: No such object: web2"), portOut: "0.0.0.0:8081", curlOut: "built with dockerfile"}
	if _, ok := checker.RunAll(context.Background(), wrongName, step4); ok {
		t.Error("이름이 web2 가 아니면 step4에서 탈락해야 함")
	}

	// 이미지가 myweb:v1 이 아님(nginx) → 탈락.
	wrongImage := docker4Fake{
		inspectOut: `[{"State":{"Status":"running"},"Config":{"Image":"nginx"}}]`,
		portOut:    "0.0.0.0:8081", curlOut: "built with dockerfile",
	}
	if _, ok := checker.RunAll(context.Background(), wrongImage, step4); ok {
		t.Error("이미지가 myweb:v1 이 아니면 step4에서 탈락해야 함")
	}

	// 8081 미매핑 → docker port 빈 출력 → 탈락.
	wrongPort := docker4Fake{inspectOut: runningMyweb, portOut: "", curlOut: "built with dockerfile"}
	if _, ok := checker.RunAll(context.Background(), wrongPort, step4); ok {
		t.Error("8081 에 매핑 안 됐으면 step4에서 탈락해야 함")
	}
}
