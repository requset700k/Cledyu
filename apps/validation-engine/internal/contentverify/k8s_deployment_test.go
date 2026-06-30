package contentverify

import (
	"context"
	"testing"

	"github.com/requset700k/cledyu/validation-engine/internal/checker"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

func k8sStep4Checks(t *testing.T) []model.Check {
	t.Helper()
	var out []model.Check
	for _, lab := range loadLabs(t) {
		if lab.ID != "lab-k8s-basics" {
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
		t.Fatal("lab-k8s-basics step4 체크를 찾지 못함")
	}
	return out
}

// step4 지문이 "nginx 이미지로" Deployment 생성을 명시하므로 web Deployment 이미지도 검증해야 한다.
// READY=4/4 만 보면 httpd 같은 이미지로 만들어도 통과한다. step4가 nginx를 보장하면 step5의
// 200 응답은 그 web Deployment(nginx)에서 오므로 step5에 별도 nginx 체크가 필요 없다(폭포수).
// (k8sPodFake 는 k8s_image_test.go 에 정의: "-o yaml" 이면 yamlOut, 아니면 getOut)
func TestK8sStep4_VerifiesNginxImage(t *testing.T) {
	step4 := k8sStep4Checks(t)
	ready := "NAME   READY   UP-TO-DATE   AVAILABLE   AGE\nweb    4/4     4            4            30s\n"

	// nginx 정답: 4/4 + 이미지 nginx
	nginx := k8sPodFake{
		getOut:  ready,
		yamlOut: "spec:\n  template:\n    spec:\n      containers:\n      - name: nginx\n        image: nginx\n",
	}
	if _, ok := checker.RunAll(context.Background(), nginx, step4); !ok {
		t.Error("nginx 이미지 + 4/4 정답은 step4를 통과해야 함")
	}

	// httpd 오답: 4/4 이지만 이미지가 틀림 → 탈락해야 한다.
	httpd := k8sPodFake{
		getOut:  ready,
		yamlOut: "spec:\n  template:\n    spec:\n      containers:\n      - name: nginx\n        image: httpd\n",
	}
	if _, ok := checker.RunAll(context.Background(), httpd, step4); ok {
		t.Error("이미지가 nginx 가 아니면(httpd) step4에서 탈락해야 함")
	}
}
