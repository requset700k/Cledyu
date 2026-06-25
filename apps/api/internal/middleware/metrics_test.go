package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsRouter는 Metrics 미들웨어 + 라우트 템플릿 두 개(/x, /items/:id) +
// /metrics 노출 핸들러를 구성한다. 라우트 템플릿을 path 라벨로 쓰는지(:id 같은
// path 파라미터가 라벨 카디널리티를 폭발시키지 않는지) 검증하기 위함이다.
func metricsRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Metrics())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/items/:id", func(c *gin.Context) { c.Status(http.StatusNotFound) })
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	return r
}

func doGet(r *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestMetricsMiddlewareExposesRED(t *testing.T) {
	r := metricsRouter()

	// 요청을 흘려보내 카운터/히스토그램을 증가시킨다.
	doGet(r, "/x")
	doGet(r, "/items/42")

	body := doGet(r, "/metrics").Body.String()

	// RED 메트릭 두 종이 노출되어야 한다.
	wantSeries := []string{
		`http_requests_total{`,
		`http_request_duration_seconds_bucket{`,
		`method="GET"`,
		`path="/x"`,
		`status="200"`,
		`status="404"`,
	}
	for _, want := range wantSeries {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics 출력에 %q 가 없음\n--- body ---\n%s", want, body)
		}
	}

	// path 파라미터는 라우트 템플릿으로 집계되어야 한다(카디널리티 제한):
	// /items/42 → path="/items/:id", 절대 path="/items/42" 가 아니어야 한다.
	if !strings.Contains(body, `path="/items/:id"`) {
		t.Errorf("path 라벨이 라우트 템플릿(/items/:id)으로 집계되지 않음\n--- body ---\n%s", body)
	}
	if strings.Contains(body, `path="/items/42"`) {
		t.Errorf("path 라벨에 raw path 파라미터(42)가 새어나옴 — 카디널리티 폭발 위험")
	}

	// /metrics 자체는 집계 대상에서 제외되어야 한다(자기 스크랩 노이즈 방지).
	if strings.Contains(body, `path="/metrics"`) {
		t.Errorf("/metrics 엔드포인트가 자기 자신을 집계함 — 제외되어야 함")
	}
}

// 프로덕션 미들웨어 순서(Recovery 바깥, Metrics 안쪽)를 그대로 재현한다.
// 핸들러가 panic 하면 바깥 Recovery 가 500 을 쓰는데, Metrics 가 이 500 을
// http_requests_total{status="500"} 과 duration 히스토그램에 기록해야 한다.
// 기록하지 않으면 RED 오류율/지연 SLI 가 실제 장애를 낮게 본다.
func TestMetricsRecordsPanicAs500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(Metrics())
	r.GET("/boom", func(_ *gin.Context) { panic("boom") })
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// panic 핸들러 호출 — Recovery 가 500 으로 변환한다.
	if got := doGet(r, "/boom").Code; got != http.StatusInternalServerError {
		t.Fatalf("panic 경로 응답코드: got %d, want 500", got)
	}

	body := doGet(r, "/metrics").Body.String()
	if !strings.Contains(body, `path="/boom"`) || !strings.Contains(body, `status="500"`) {
		t.Errorf("panic→500 이 메트릭에 기록되지 않음 (RED 오류율 누락)\n--- body ---\n%s", body)
	}
}
