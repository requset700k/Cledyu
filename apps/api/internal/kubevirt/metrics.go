package kubevirt

import (
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	vmBootTotal        *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	vmBootTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vm_boot_total",
			Help: "VM 부팅 결과 카운터(result=success|failed).",
		},
		[]string{"result", "env"},
	)
	reg.MustRegister(vmBootTotal)
	return &metrics{
		vmBootTotal:        vmBootTotal,
	}
}