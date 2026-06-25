package middleware

import (
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
func Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		// FullPath()는 매칭된 라우트 템플릿(예: "/api/v1/sessions/:id")을 반환한다.
		// 매칭되지 않은 요청(404)은 빈 문자열이므로 단일 라벨로 묶어 카디널리티를 막는다.
		path := c.FullPath()
		if path == "/metrics" {
			return
		}
		if path == "" {
			path = "<unmatched>"
		}

		method := c.Request.Method
		status := strconv.Itoa(c.Writer.Status())

		httpRequestsTotal.WithLabelValues(method, path, status).Inc()
		httpRequestDuration.WithLabelValues(method, path, status).Observe(time.Since(start).Seconds())
	}
}
