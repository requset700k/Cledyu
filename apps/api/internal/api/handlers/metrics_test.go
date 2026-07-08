package handlers

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestHandlerMetricsExposeZeroValueSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	newHandlerMetrics(reg)

	tests := map[string]int{
		"ws_connection_established_total": 2,
		"ws_connection_drop_total":        4,
		"validation_duration_seconds":     2,
		"ai_hint_latency_seconds":         4,
	}
	for name, want := range tests {
		got, err := testutil.GatherAndCount(reg, name)
		if err != nil {
			t.Fatalf("gather %s: %v", name, err)
		}
		if got != want {
			t.Errorf("%s series = %d, want %d", name, got, want)
		}
	}
}
