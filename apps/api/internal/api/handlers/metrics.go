package handlers

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type handlerMetrics struct {
	wsConnectionsEstablished *prometheus.CounterVec
	wsConnectionDrops        *prometheus.CounterVec
	validationDuration       *prometheus.HistogramVec
	validationStartTimes     map[string]time.Time
	validationMu             sync.Mutex
}

func newHandlerMetrics(reg prometheus.Registerer) *handlerMetrics {
	wsConnectionsEstablished := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ws_connection_established_total",
			Help: "WebSocket 연결 수립 횟수.",
		},
		[]string{"provider"},
	)
	wsConnectionDrops := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ws_connection_drop_total",
			Help: "WebSocket 연결 끊김 횟수.",
		},
		[]string{"provider"},
	)
	validationDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "validation_duration_seconds",
			Help:    "ValidateStep 요청 발행부터 결과 수신까지 걸린 시간(초).",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30},
		},
		[]string{"result"}, // passed | failed
	)
	reg.MustRegister(wsConnectionsEstablished, wsConnectionDrops, validationDuration)
	return &handlerMetrics{
		wsConnectionsEstablished: wsConnectionsEstablished,
		wsConnectionDrops:        wsConnectionDrops,
		validationDuration:       validationDuration,
		validationStartTimes:     make(map[string]time.Time),
	}
}