package handlers

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestHandlerMetricsExposeZeroValueSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	newHandlerMetrics(reg)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	tests := map[string]int{
		"ws_connection_established_total": 2,
		"ws_connection_drop_total":        4,
		"validation_duration_seconds":     2,
		"ai_hint_latency_seconds":         4,
	}
	for name, want := range tests {
		if got := metricFamilyLen(families, name); got != want {
			t.Errorf("%s series = %d, want %d", name, got, want)
		}
	}
}

func metricFamilyLen(families []*dto.MetricFamily, name string) int {
	for _, family := range families {
		if family.GetName() == name {
			return len(family.GetMetric())
		}
	}
	return 0
}
