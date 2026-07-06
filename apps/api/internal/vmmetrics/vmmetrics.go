// Package vmmetrics는 온프렘(KubeVirt)·EC2 오버플로우 두 부팅 경로가 공유하는
// vm_boot_total 카운터를 정의
package vmmetrics

import "github.com/prometheus/client_golang/prometheus"

const (
	// ResultSuccess/ResultFailed는 RecordBoot의 result 라벨 값이다.
	ResultSuccess = "success"
	ResultFailed  = "failed"
)

// Recorder는 vm_boot_total 카운터에 대한 유일한 쓰기 경로다.
type Recorder struct {
	vmBootTotal *prometheus.CounterVec
}

// New는 주어진 Registerer에 vm_boot_total을 등록한 Recorder를 반환
func New(reg prometheus.Registerer) *Recorder {
	vmBootTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vm_boot_total",
			Help: "VM 부팅 결과 카운터(result=success|failed, env=kubevirt|ec2).",
		},
		[]string{"result", "env"},
	)
	reg.MustRegister(vmBootTotal)
	for _, result := range []string{ResultSuccess, ResultFailed} {
		for _, env := range []string{"kubevirt", "ec2"} {
			vmBootTotal.WithLabelValues(result, env)
		}
	}
	return &Recorder{vmBootTotal: vmBootTotal}
}

// RecordBoot은 부팅 결과 1건을 기록
func (r *Recorder) RecordBoot(result, env string) {
	if r == nil {
		return
	}
	r.vmBootTotal.WithLabelValues(result, env).Inc()
}

// Collector는 테스트에서 testutil.CollectAndCount 등으로 직접 검증할 때만 사용
func (r *Recorder) Collector() *prometheus.CounterVec {
	return r.vmBootTotal
}
