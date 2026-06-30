package contentverify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/checker"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// k8sPodFake는 명령에 따라 다른 출력을 주는 테스트용 executor다.
//   - "kubectl get pod nginx -o yaml" → pod 매니페스트(yaml)
//   - 그 외 "kubectl get pod nginx"   → get 출력(STATUS 포함)
type k8sPodFake struct {
	getOut  string
	yamlOut string
}

func (k k8sPodFake) Exec(_ context.Context, cmd string) (string, error) {
	if strings.Contains(cmd, "-o yaml") {
		return k.yamlOut, nil
	}
	return k.getOut, nil
}
func (k k8sPodFake) DefaultTimeout() time.Duration { return 20 * time.Second }
func (k k8sPodFake) Close()                        {}

func k8sStep2Checks(t *testing.T) []model.Check {
	t.Helper()
	var out []model.Check
	for _, lab := range loadLabs(t) {
		if lab.ID != "lab-k8s-basics" {
			continue
		}
		for _, s := range lab.Steps {
			if s.ID != 2 {
				continue
			}
			for _, wc := range s.Checks {
				out = append(out, toModelCheck(t, wc))
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("lab-k8s-basics step2 체크를 찾지 못함")
	}
	return out
}

// step2는 "nginx:alpine(경량) 이미지로" 단일 Pod 실행을 명시한다. STATUS=Running 만 보면 httpd
// 는 물론 무거운 plain nginx 로도 통과해 학습자가 틀린 걸 모른다. 교육 목적상 가장 경량인
// nginx:alpine 을 강제하려고 이미지 태그까지 검증한다(substring 이라 plain nginx 도 탈락).
func TestK8sStep2_VerifiesNginxImage(t *testing.T) {
	step2 := k8sStep2Checks(t)
	running := "NAME    READY   STATUS    RESTARTS   AGE\nnginx   1/1     Running   0          5s\n"

	// nginx:alpine 정답: Running + 경량 이미지 → 통과해야 한다.
	alpine := k8sPodFake{
		getOut:  running,
		yamlOut: "spec:\n  containers:\n  - name: nginx\n    image: nginx:alpine\n",
	}
	if _, ok := checker.RunAll(context.Background(), alpine, step2); !ok {
		t.Error("nginx:alpine 이미지 정답은 step2를 통과해야 함")
	}

	// plain nginx 오답: 경량 태그가 아님 → 탈락해야 한다(가장 경량 이미지 사용 강제).
	plain := k8sPodFake{
		getOut:  running,
		yamlOut: "spec:\n  containers:\n  - name: nginx\n    image: nginx\n",
	}
	if _, ok := checker.RunAll(context.Background(), plain, step2); ok {
		t.Error("경량 태그가 아니면(plain nginx) step2에서 탈락해야 함")
	}

	// httpd 오답: Running 이지만 이미지가 틀림 → 탈락해야 한다(학습자에게 이미지 오류 알림).
	httpd := k8sPodFake{
		getOut:  running,
		yamlOut: "spec:\n  containers:\n  - name: nginx\n    image: httpd\n",
	}
	if _, ok := checker.RunAll(context.Background(), httpd, step2); ok {
		t.Error("이미지가 nginx:alpine 이 아니면(httpd) step2에서 탈락해야 함")
	}
}
