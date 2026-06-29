package handlers

import (
	"github.com/prometheus/client_golang/prometheus"
)

type handlerMetrics struct {
	wsConnectionsEstablished *prometheus.CounterVec
	wsConnectionDrops        *prometheus.CounterVec
}

func newHandlerMetrics(reg prometheus.Registerer) *handlerMetrics {
	wsConnectionsEstablished := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ws_connection_established_total",
			Help: "WebSocket 연결 수립 횟수.",
		},
		[]string{"provider"}, // kubevirt | ec2
	)
	wsConnectionDrops := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ws_connection_drop_total",
			Help: "WebSocket 연결 끊김 횟수.",
		},
		[]string{"provider"}, // kubevirt | ec2
	)
	reg.MustRegister(wsConnectionsEstablished, wsConnectionDrops)
	return &handlerMetrics{
		wsConnectionsEstablished: wsConnectionsEstablished,
		wsConnectionDrops:        wsConnectionDrops,
	}
}