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

// step2는 "nginx 이미지로" 단일 Pod 실행을 명시한다. STATUS=Running 만 보면 httpd 같은
// 엉뚱한 이미지로도 통과해 학습자가 틀린 걸 모른다. 이미지가 nginx 인지도 검증해야 한다.
func TestK8sStep2_VerifiesNginxImage(t *testing.T) {
	step2 := k8sStep2Checks(t)
	running := "NAME    READY   STATUS    RESTARTS   AGE\nnginx   1/1     Running   0          5s\n"

	// nginx 정답: Running + 이미지 nginx → 통과해야 한다.
	nginx := k8sPodFake{
		getOut:  running,
		yamlOut: "spec:\n  containers:\n  - name: nginx\n    image: nginx\n",
	}
	if _, ok := checker.RunAll(context.Background(), nginx, step2); !ok {
		t.Error("nginx 이미지 정답은 step2를 통과해야 함")
	}

	// httpd 오답: Running 이지만 이미지가 틀림 → 탈락해야 한다(학습자에게 이미지 오류 알림).
	httpd := k8sPodFake{
		getOut:  running,
		yamlOut: "spec:\n  containers:\n  - name: nginx\n    image: httpd\n",
	}
	if _, ok := checker.RunAll(context.Background(), httpd, step2); ok {
		t.Error("이미지가 nginx 가 아니면(httpd) step2에서 탈락해야 함")
	}
}
