package middleware

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// excludedFromTracing는 트레이스를 생성하지 않을 경로 목록
var excludedFromTracing = map[string]bool{
	"/health":  true,
	"/ready":   true,
	"/metrics": true,
}

func Tracing(serviceName string) gin.HandlerFunc {
	return otelgin.Middleware(serviceName,
		otelgin.WithFilter(func(r *http.Request) bool {
			return !excludedFromTracing[r.URL.Path]
		}),
	)
}