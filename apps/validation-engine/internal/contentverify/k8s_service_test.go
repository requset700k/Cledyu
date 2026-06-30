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

// k8sSvcFake는 명령에 따라 다른 결과를 주는 테스트용 executor다.
//   - "kubectl get service web"            → 서비스 목록(없으면 NotFound 에러)
//   - "curl ... %{http_code} ..."          → HTTP 응답 코드
type k8sSvcFake struct {
	svcOut   string
	svcErr   error
	httpCode string
}

func (k k8sSvcFake) Exec(_ context.Context, cmd string) (string, error) {
	switch {
	case strings.Contains(cmd, "get service web"):
		return k.svcOut, k.svcErr
	case strings.Contains(cmd, "http_code"):
		return k.httpCode, nil
	default:
		return "", nil
	}
}
func (k k8sSvcFake) DefaultTimeout() time.Duration { return 20 * time.Second }
func (k k8sSvcFake) Close()                        {}

func k8sStep5Checks(t *testing.T) []model.Check {
	t.Helper()
	var out []model.Check
	for _, lab := range loadLabs(t) {
		if lab.ID != "lab-k8s-basics" {
			continue
		}
		for _, s := range lab.Steps {
			if s.ID != 5 {
				continue
			}
			for _, wc := range s.Checks {
				out = append(out, toModelCheck(t, wc))
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("lab-k8s-basics step5 체크를 찾지 못함")
	}
	return out
}

// step5는 "NodePort Service 로 web Deployment 를 30080 에 노출"을 명시한다. 200 만 보면
// python3 -m http.server 30080 처럼 서비스 없이 30080 에 200 만 띄워도 통과한다(절차 우회).
// 그래서 web 이름의 서비스가 30080 을 노출하는지도 검증해야 한다.
func TestK8sStep5_RequiresNodePortService(t *testing.T) {
	step5 := k8sStep5Checks(t)
	svcListing := "NAME   TYPE       CLUSTER-IP   EXTERNAL-IP   PORT(S)          AGE\n" +
		"web    NodePort   10.43.0.5    <none>        80:30080/TCP     5s\n"

	// 정답: web NodePort 서비스(30080) 존재 + 200
	good := k8sSvcFake{svcOut: svcListing, httpCode: "200"}
	if _, ok := checker.RunAll(context.Background(), good, step5); !ok {
		t.Error("web NodePort 서비스(30080) + 200 정답은 step5를 통과해야 함")
	}

	// 우회: 서비스 없이 30080 에 200 만 (python http.server 등) → get service web 은 NotFound
	cheat := k8sSvcFake{svcOut: "", svcErr: errors.New("Error from server (NotFound): services \"web\" not found"), httpCode: "200"}
	if _, ok := checker.RunAll(context.Background(), cheat, step5); ok {
		t.Error("web 서비스 없이 30080 에 200 만 있으면 step5에서 탈락해야 함(절차 우회 차단)")
	}

	// 우회: 30080 은 노출하지만 타입이 NodePort 가 아님(LoadBalancer) → 탈락해야 한다.
	// (문제가 'NodePort Service' 를 명시하므로 타입까지 검증)
	lb := k8sSvcFake{
		svcOut: "NAME   TYPE           CLUSTER-IP   EXTERNAL-IP   PORT(S)        AGE\n" +
			"web    LoadBalancer   10.43.0.5    192.168.1.5   80:30080/TCP   5s\n",
		httpCode: "200",
	}
	if _, ok := checker.RunAll(context.Background(), lb, step5); ok {
		t.Error("타입이 NodePort 가 아니면(LoadBalancer) step5에서 탈락해야 함")
	}
}
