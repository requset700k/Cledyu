package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RED(Rate/Errors/Duration) 기본 세트. SLO 담당자가 도메인 SLI(세션 생성 성공률,
// validate 지연 등)를 이 위에 얹어 세부 조정한다. 라벨은 method/path/status 3종으로
// 고정 — path 는 raw URL 이 아니라 Gin 라우트 템플릿(c.FullPath())을 써서
// /sessions/:id 처럼 path 파라미터가 시계열 카디널리티를 폭발시키는 것을 막는다.
var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "처리한 HTTP 요청 수(method/path/status 별).",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP 요청 처리 지연(초). 기본 버킷 — latency SLI 의 분위수 산정에 사용.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path", "status"},
	)
)

// Metrics는 RED 메트릭을 기록하는 Gin 미들웨어다. router 의 Logger 뒤에 둔다.
// /metrics 엔드포인트 자신은 집계에서 제외해 스크랩 노이즈를 만들지 않는다.
//
// 관측은 defer 로 수행한다. 핸들러가 panic 하면 바깥 gin.Recovery 가 500 을 쓰지만,
// 그 시점은 이 defer 가 돈 뒤라 c.Writer.Status() 는 아직 500 이 아니다. 따라서 panic
// 경로는 명시적으로 500 으로 기록하고 panic 을 다시 던져 Recovery 가 정상적으로 500 응답을
// 쓰게 한다. 이렇게 하면 미들웨어 등록 순서와 무관하게 panic→500 이 RED 오류율에 잡힌다.
func Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		defer func() {
			rec := recover()

			// FullPath()는 매칭된 라우트 템플릿(예: "/api/v1/sessions/:id")을 반환한다.
			// 매칭되지 않은 요청(404)은 빈 문자열이므로 단일 라벨로 묶어 카디널리티를 막는다.
			path := c.FullPath()
			if path == "/metrics" {
				if rec != nil {
					panic(rec) // 집계는 건너뛰되 panic 전파는 막지 않는다.
				}
				return
			}
			if path == "" {
				path = "<unmatched>"
			}

			method := c.Request.Method
			status := c.Writer.Status()
			if rec != nil {
				status = http.StatusInternalServerError
			}
			statusStr := strconv.Itoa(status)

			httpRequestsTotal.WithLabelValues(method, path, statusStr).Inc()
			httpRequestDuration.WithLabelValues(method, path, statusStr).Observe(time.Since(start).Seconds())

			if rec != nil {
				panic(rec) // 바깥 Recovery 가 500 응답을 쓰도록 다시 던진다.
			}
		}()

		c.Next()
	}
}
