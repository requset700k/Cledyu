package contentverify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/checker"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// dockerFake는 명령에 따라 다른 결과를 주는 테스트용 executor다.
//   - "docker inspect web"  → 컨테이너 JSON(State.Status, Config.Image)
//   - "docker port web 80"  → 80 포트의 호스트 매핑(미매핑이면 빈 출력)
//   - "... http_code ..."   → curl HTTP 코드
type dockerFake struct {
	inspectOut string
	portOut    string
	httpCode   string
}

func (d dockerFake) Exec(_ context.Context, cmd string) (string, error) {
	switch {
	case strings.Contains(cmd, "docker inspect web"):
		return d.inspectOut, nil
	case strings.Contains(cmd, "docker port web"):
		return d.portOut, nil
	case strings.Contains(cmd, "http_code"):
		return d.httpCode, nil
	default:
		return "", nil
	}
}
func (d dockerFake) DefaultTimeout() time.Duration { return 20 * time.Second }
func (d dockerFake) Close()                        {}

func dockerStep1Checks(t *testing.T) []model.Check {
	t.Helper()
	var out []model.Check
	for _, lab := range loadLabs(t) {
		if lab.ID != "lab-docker-basics" {
			continue
		}
		for _, s := range lab.Steps {
			if s.ID != 1 {
				continue
			}
			for _, wc := range s.Checks {
				out = append(out, toModelCheck(t, wc))
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("lab-docker-basics step1 체크를 찾지 못함")
	}
	return out
}

// step1 지문은 "nginx 이미지" + "호스트 8080 → 컨테이너 80" 을 명시한다. running + 200 만 보면
// httpd 로 만들거나(이미지) -p 없이 다른 8080 서버를 띄워도(포트) 통과한다. 그러면 step2
// (nginx 웹루트 /usr/share/nginx/html 에 쓰고 curl 8080 으로 확인)에서 뒤늦게 막히는데, 복귀
// 불가라 step1 로 돌아가 못 고친다(dead-end). 그래서 이미지·포트를 step1 에서 검증해야 한다.
func TestDockerStep1_VerifiesImageAndPort(t *testing.T) {
	step1 := dockerStep1Checks(t)
	runningNginx := `[{"State":{"Status":"running"},"Config":{"Image":"nginx"}}]`

	// 정답: nginx + 컨테이너80→호스트8080 + 200
	good := dockerFake{inspectOut: runningNginx, portOut: "0.0.0.0:8080", httpCode: "200"}
	if _, ok := checker.RunAll(context.Background(), good, step1); !ok {
		t.Error("nginx + 8080:80 + 200 정답은 step1을 통과해야 함")
	}

	// httpd 이미지: 이미지가 틀림 → 탈락해야 한다(step2 의 nginx 경로 dead-end 방지).
	httpd := dockerFake{
		inspectOut: `[{"State":{"Status":"running"},"Config":{"Image":"httpd"}}]`,
		portOut:    "0.0.0.0:8080", httpCode: "200",
	}
	if _, ok := checker.RunAll(context.Background(), httpd, step1); ok {
		t.Error("이미지가 nginx 가 아니면(httpd) step1에서 탈락해야 함")
	}

	// 포트 미매핑: 컨테이너 80 이 8080 에 안 붙음(-p 누락 + 다른 8080 서버) → docker port 빈 출력 → 탈락.
	noPort := dockerFake{inspectOut: runningNginx, portOut: "", httpCode: "200"}
	if _, ok := checker.RunAll(context.Background(), noPort, step1); ok {
		t.Error("컨테이너 80 이 호스트 8080 에 매핑 안 됐으면 step1에서 탈락해야 함")
	}
}
