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

// helmFake는 helm 명령별로 결과를 반환한다.
type helmFake struct {
	versionOut   string
	repoListOut  string
	lintOut      string
	statusOut    string
	metadataOut  string
	showChartOut string
	showChartErr error
	valuesOut    string
	historyOut   string
}

func (h helmFake) Exec(_ context.Context, cmd string) (string, error) {
	switch {
	case strings.Contains(cmd, "helm version"):
		return h.versionOut, nil
	case strings.Contains(cmd, "helm repo list"):
		return h.repoListOut, nil
	case strings.Contains(cmd, "helm lint"):
		return h.lintOut, nil
	case strings.Contains(cmd, "helm status"):
		return h.statusOut, nil
	case strings.Contains(cmd, "helm get metadata"):
		return h.metadataOut, nil
	case strings.Contains(cmd, "helm show chart"):
		return h.showChartOut, h.showChartErr
	case strings.Contains(cmd, "helm get values"):
		return h.valuesOut, nil
	case strings.Contains(cmd, "helm history"):
		return h.historyOut, nil
	default:
		return "", nil
	}
}
func (h helmFake) DefaultTimeout() time.Duration { return 20 * time.Second }
func (h helmFake) Close()                        {}

// metaYAML 은 helm get metadata web -o yaml 의 실제 출력을 모사한다(로컬 v4.2.2 실측:
// chart 키 아래 차트 이름이 그대로, 값 줄은 줄바꿈으로 끝난다). expect 는 "chart: <이름>\n"
// 로 정확매칭하므로, chart 이름이 mychart2·notmychart 처럼 mychart 를 포함해도 탈락해야 한다.
func metaYAML(chart string) string {
	return "appVersion: 1.16.0\nchart: " + chart + "\nname: web\nnamespace: default\nrevision: 1\nstatus: deployed\nversion: 0.1.0\n"
}

func helmChecks(t *testing.T, stepID int) []model.Check {
	t.Helper()
	var out []model.Check
	for _, lab := range loadLabs(t) {
		if lab.ID != "lab-helm-advanced" {
			continue
		}
		for _, s := range lab.Steps {
			if s.ID != stepID {
				continue
			}
			for _, wc := range s.Checks {
				out = append(out, toModelCheck(t, wc))
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("lab-helm-advanced step%d 체크를 찾지 못함", stepID)
	}
	return out
}

// step2 지문은 "helm create 로 mychart 를 생성"하라고 명시한다. helm create 가 만드는 파일
// 전부를 step2 에서 검증해야 한다. 일부만 확인하면 나머지를 삭제·미생성해도 통과하고,
// step3 lint 에서 막히면 step2 로 돌아갈 수 없다(폭포수 dead-end).
func TestHelmStep2_VerifiesAllCreatedFiles(t *testing.T) {
	step2 := helmChecks(t, 2)

	// file_exists 체크는 executor.Exec 가 아닌 직접 파일 시스템 확인이라 fake 불필요.
	// contentverify 의 file_exists 는 loadLabs → toModelCheck 로 check 타입 확인 후
	// checker.Run 이 실행 → file_exists 는 실제 stat 이므로, 여기서는 YAML 에 체크가
	// 모두 선언됐는지(개수·경로)만 검증한다.
	wantPaths := []string{
		"/home/lab/mychart/.helmignore",
		"/home/lab/mychart/Chart.yaml",
		"/home/lab/mychart/values.yaml",
		"/home/lab/mychart/templates/NOTES.txt",
		"/home/lab/mychart/templates/_helpers.tpl",
		"/home/lab/mychart/templates/deployment.yaml",
		"/home/lab/mychart/templates/hpa.yaml",
		"/home/lab/mychart/templates/httproute.yaml",
		"/home/lab/mychart/templates/ingress.yaml",
		"/home/lab/mychart/templates/service.yaml",
		"/home/lab/mychart/templates/serviceaccount.yaml",
		"/home/lab/mychart/templates/tests/test-connection.yaml",
	}

	got := map[string]bool{}
	for _, c := range step2 {
		got[c.Path] = true
	}
	for _, want := range wantPaths {
		if !got[want] {
			t.Errorf("step2 체크에 %s 가 없음 — helm create 생성 파일 누락", want)
		}
	}
	if t.Failed() {
		t.Logf("현재 step2 체크 경로: %v", func() []string {
			var s []string
			for _, c := range step2 {
				s = append(s, c.Path)
			}
			return s
		}())
	}
}

// step3 지문은 "helm package 로 .tgz 패키징"을 명시한다. file_exists 만으론 touch 로 만든
// 빈 파일도 통과한다(실제 패키징 안 함). helm show chart 로 .tgz 가 진짜 helm 패키지이고
// 내용이 mychart 인지 확인한다(위조 .tgz 는 "not a gzipped archive" 에러로 탈락).
func TestHelmStep3_VerifiesRealPackage(t *testing.T) {
	step3 := helmChecks(t, 3)

	// 회귀 가드: .tgz 체크가 helm show chart(진위검증)를 쓰도록 고정.
	var showCmd string
	for _, c := range step3 {
		if strings.Contains(c.Command, "helm show chart") {
			showCmd = c.Command
		}
	}
	if !strings.Contains(showCmd, "mychart-0.1.0.tgz") {
		t.Errorf("step3 는 helm show chart 로 .tgz 진위를 검증해야 함 (현재: %q)", showCmd)
	}

	lintOK := "==> Linting mychart\n1 chart(s) linted, 0 chart(s) failed"

	// 정답: lint 통과 + 진짜 패키지(name: mychart)
	good := helmFake{
		lintOut:      lintOK,
		showChartOut: "apiVersion: v2\nname: mychart\ndescription: A Helm chart\nversion: 0.1.0\n",
	}
	if _, ok := checker.RunAll(context.Background(), good, step3); !ok {
		t.Error("진짜 mychart 패키지는 step3을 통과해야 함")
	}

	// touch 위조 .tgz → helm show chart 가 gzip 아님 에러 → 탈락.
	fakeTgz := helmFake{lintOut: lintOK, showChartErr: errors.New("Error: file does not appear to be a gzipped archive")}
	if _, ok := checker.RunAll(context.Background(), fakeTgz, step3); ok {
		t.Error("touch 로 만든 빈 .tgz 는 step3에서 탈락해야 함 — 진위 검증")
	}

	// 다른 차트를 mychart-0.1.0.tgz 로 위장 → 내부 name 이 mychart 아님 → 탈락.
	wrongChart := helmFake{lintOut: lintOK, showChartOut: "apiVersion: v2\nname: otherchart\nversion: 0.1.0\n"}
	if _, ok := checker.RunAll(context.Background(), wrongChart, step3); ok {
		t.Error("내부 차트가 mychart 아니면 step3에서 탈락해야 함")
	}
}

// step4 지문은 "mychart 를 web 이라는 릴리스 이름으로 설치"하라고 명시한다.
// 차트 검증은 helm get metadata web(릴리스를 정확히 지정) 으로 web 의 CHART 를 본다.
// helm list -f web 은 -f 가 정규식(앵커 불가, ^web$ 는 화이트리스트 밖)이라 web2 같은
// 형제 릴리스가 mychart 면 false-positive 가 된다 — get metadata 는 릴리스 한정이라 안전하다.
func TestHelmStep4_VerifiesChartName(t *testing.T) {
	step4 := helmChecks(t, 4)

	// 회귀 가드: 차트 체크가 릴리스 한정 명령(helm get metadata web)을 쓰도록 고정.
	// helm list -f 정규식으로 되돌리면 형제 릴리스 false-positive 가 되므로 여기서 잡는다.
	var metaCmd string
	for _, c := range step4 {
		if strings.Contains(c.Command, "helm get metadata") {
			metaCmd = c.Command
		}
	}
	if !strings.Contains(metaCmd, "helm get metadata web") {
		t.Errorf("step4 차트 체크는 helm get metadata web(릴리스 한정)을 써야 함 (현재: %q)", metaCmd)
	}

	status := "NAME: web\nSTATUS: deployed\nREVISION: 1"

	// 정답: deployed + web 의 CHART 가 mychart
	good := helmFake{statusOut: status, metadataOut: metaYAML("mychart")}
	if _, ok := checker.RunAll(context.Background(), good, step4); !ok {
		t.Error("deployed + mychart 정답은 step4를 통과해야 함")
	}

	// 엉뚱한 차트(bitnami/nginx)로 설치 → web 의 CHART 가 mychart 아님 → 탈락.
	wrongChart := helmFake{statusOut: status, metadataOut: metaYAML("nginx")}
	if _, ok := checker.RunAll(context.Background(), wrongChart, step4); ok {
		t.Error("mychart 가 아닌 차트로 설치하면 step4에서 탈락해야 함")
	}

	// mychart 를 포함하는 다른 차트(mychart2)로 설치 → 부분매칭으로 통과하면 안 됨(줄바꿈 경계).
	lookalikeChart := helmFake{statusOut: status, metadataOut: metaYAML("mychart2")}
	if _, ok := checker.RunAll(context.Background(), lookalikeChart, step4); ok {
		t.Error("mychart2 차트로 설치하면 step4에서 탈락해야 함 — 부분매칭 방지")
	}
}

// step1 지문은 "bitnami 라는 이름으로 저장소를 추가"하라고 명시한다. 텍스트 출력은 이름이
// URL("charts.bitnami.com")에도 들어가 이름/URL 을 따로 못 묶는다. helm repo list -o json 의
// {"name":...,"url":...} 인접쌍으로 이름(bitnami)과 URL 을 함께 정확검증한다.
func TestHelmStep1_VerifiesRepoNameAndURL(t *testing.T) {
	step1 := helmChecks(t, 1)
	v3 := `version.BuildInfo{Version:"v3.21.2"}`

	// 정답: 이름 bitnami + 올바른 URL
	good := helmFake{
		versionOut:  v3,
		repoListOut: `[{"name":"bitnami","url":"https://charts.bitnami.com/bitnami"}]`,
	}
	if _, ok := checker.RunAll(context.Background(), good, step1); !ok {
		t.Error("helm v3 + name bitnami + 올바른 URL 정답은 step1을 통과해야 함")
	}

	// 잘못된 URL → 탈락.
	wrongURL := helmFake{
		versionOut:  v3,
		repoListOut: `[{"name":"bitnami","url":"https://wrong.example.com/bitnami"}]`,
	}
	if _, ok := checker.RunAll(context.Background(), wrongURL, step1); ok {
		t.Error("잘못된 URL로 추가하면 step1에서 탈락해야 함 — URL 검증")
	}

	// 이름이 bitnami 가 아님(foo) + URL 만 맞음 → 탈락해야 한다(이름까지 검증).
	wrongName := helmFake{
		versionOut:  v3,
		repoListOut: `[{"name":"foo","url":"https://charts.bitnami.com/bitnami"}]`,
	}
	if _, ok := checker.RunAll(context.Background(), wrongName, step1); ok {
		t.Error("이름이 bitnami 가 아니면 step1에서 탈락해야 함 — 이름 검증")
	}
}

// step5 지문은 "mychart 로 web 을 upgrade, replicaCount 를 2 로" 명시한다.
//   - expect "replicaCount"(키만)면 값이 달라도 통과 → "replicaCount: 2" 로 값까지 검증
//   - 차트 미검증이면 엉뚱한 차트(bitnami/nginx)로 upgrade 해도 통과 → helm get metadata web 으로
//     upgrade 후 현재 차트가 여전히 mychart 인지 검증(state 는 step4 이후 바뀔 수 있으므로 재확인)
func TestHelmStep5_VerifiesValueAndChart(t *testing.T) {
	step5 := helmChecks(t, 5)

	historySuperseded := "REVISION\tSTATUS\n1\tsuperseded\n2\tdeployed"
	metaMychart := metaYAML("mychart")

	// 회귀 가드: 차트 체크가 릴리스 한정 명령(helm get metadata web)을 쓰도록 고정.
	var metaCmd string
	for _, c := range step5 {
		if strings.Contains(c.Command, "helm get metadata") {
			metaCmd = c.Command
		}
	}
	if !strings.Contains(metaCmd, "helm get metadata web") {
		t.Errorf("step5 차트 체크는 helm get metadata web(릴리스 한정)을 써야 함 (현재: %q)", metaCmd)
	}

	// helm get values web 의 실제 출력 형식(yaml, 값 줄 끝 줄바꿈)을 모사한다.
	values := func(line string) string { return "USER-SUPPLIED VALUES:\n" + line + "\n" }

	// 정답: replicaCount: 2 + superseded + mychart
	good := helmFake{valuesOut: values("replicaCount: 2"), historyOut: historySuperseded, metadataOut: metaMychart}
	if _, ok := checker.RunAll(context.Background(), good, step5); !ok {
		t.Error("replicaCount: 2 + superseded + mychart 정답은 step5를 통과해야 함")
	}

	// replicaCount 를 다른 값(1)으로 설정 → 탈락.
	wrongValue := helmFake{valuesOut: values("replicaCount: 1"), historyOut: historySuperseded, metadataOut: metaMychart}
	if _, ok := checker.RunAll(context.Background(), wrongValue, step5); ok {
		t.Error("replicaCount: 1 이면 step5에서 탈락해야 함 — 값 검증")
	}

	// replicaCount: 20 → "replicaCount: 2" 부분매칭으로 통과하면 안 됨(줄바꿈 경계로 정확매칭).
	twentyValue := helmFake{valuesOut: values("replicaCount: 20"), historyOut: historySuperseded, metadataOut: metaMychart}
	if _, ok := checker.RunAll(context.Background(), twentyValue, step5); ok {
		t.Error("replicaCount: 20 이면 step5에서 탈락해야 함 — 부분매칭 방지")
	}

	// replicaCount 키 자체가 없음 → 탈락.
	noKey := helmFake{valuesOut: "USER-SUPPLIED VALUES:\nnull\n", historyOut: historySuperseded, metadataOut: metaMychart}
	if _, ok := checker.RunAll(context.Background(), noKey, step5); ok {
		t.Error("replicaCount 없으면 step5에서 탈락해야 함")
	}

	// 엉뚱한 차트(nginx)로 upgrade → web 의 CHART 가 mychart 아님 → 탈락(값은 정답이라 차트만 격리).
	wrongChart := helmFake{
		valuesOut: values("replicaCount: 2"), historyOut: historySuperseded,
		metadataOut: metaYAML("nginx"),
	}
	if _, ok := checker.RunAll(context.Background(), wrongChart, step5); ok {
		t.Error("mychart 가 아닌 차트로 upgrade 하면 step5에서 탈락해야 함 — 차트 fidelity")
	}

	// mychart 를 포함하는 다른 차트(mychart2)로 upgrade → 부분매칭으로 통과하면 안 됨(줄바꿈 경계).
	lookalikeChart := helmFake{
		valuesOut: values("replicaCount: 2"), historyOut: historySuperseded,
		metadataOut: metaYAML("mychart2"),
	}
	if _, ok := checker.RunAll(context.Background(), lookalikeChart, step5); ok {
		t.Error("mychart2 차트로 upgrade 하면 step5에서 탈락해야 함 — 부분매칭 방지")
	}
}
