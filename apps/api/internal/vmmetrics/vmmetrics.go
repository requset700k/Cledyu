// Package vmmetrics는 온프렘(KubeVirt)·EC2 오버플로우 두 부팅 경로가 공유하는
// vm_boot_total 카운터를 정의
package vmmetrics

import "github.com/prometheus/client_golang/prometheus"

const (
	// ResultSuccess/ResultFailed는 RecordBoot의 result 라벨 값이다.
	ResultSuccess = "success"
	ResultFailed  = "failed"

	LabEnvOnprem = "onprem"
	LabEnvEC2    = "ec2"

	LabReasonReady    = "ready"
	LabReasonVMFailed = "vm_failed"
	LabReasonTimeout  = "timeout"
)

// Recorder는 vm_boot_total 카운터에 대한 유일한 쓰기 경로다.
type Recorder struct {
	vmBootTotal        *prometheus.CounterVec
	labStartTotal      *prometheus.CounterVec
	labStartupDuration *prometheus.HistogramVec
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
	labStartTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lab_start_total",
			Help: "Lab 시작 결과 카운터(result=success|failed, reason=ready|vm_failed|timeout, env=onprem|ec2).",
		},
		[]string{"result", "env", "reason"},
	)
	labStartupDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "lab_startup_duration_seconds",
			Help:    "CreateSession부터 VM Ready/running 또는 실패 판정까지 걸린 시간(초).",
			Buckets: []float64{30, 60, 120, 180, 300, 420, 600, 900, 1200},
		},
		[]string{"result", "env"},
	)
	reg.MustRegister(vmBootTotal, labStartTotal, labStartupDuration)
	for _, result := range []string{ResultSuccess, ResultFailed} {
		for _, env := range []string{"kubevirt", "ec2"} {
			vmBootTotal.WithLabelValues(result, env)
		}
	}
	for _, env := range []string{LabEnvOnprem, LabEnvEC2} {
		labStartTotal.WithLabelValues(ResultSuccess, env, LabReasonReady)
		labStartTotal.WithLabelValues(ResultFailed, env, LabReasonVMFailed)
		labStartTotal.WithLabelValues(ResultFailed, env, LabReasonTimeout)
		for _, result := range []string{ResultSuccess, ResultFailed} {
			labStartupDuration.WithLabelValues(result, env)
		}
	}
	return &Recorder{
		vmBootTotal:        vmBootTotal,
		labStartTotal:      labStartTotal,
		labStartupDuration: labStartupDuration,
	}
}

// RecordBoot은 부팅 결과 1건을 기록
func (r *Recorder) RecordBoot(result, env string) {
	if r == nil {
		return
	}
	r.vmBootTotal.WithLabelValues(result, env).Inc()
}

// RecordLabStart는 Lab 시작 결과와 시작 후 경과 시간을 기록한다.
func (r *Recorder) RecordLabStart(result, env, reason string, durationSeconds float64) {
	if r == nil {
		return
	}
	r.labStartTotal.WithLabelValues(result, env, reason).Inc()
	if durationSeconds >= 0 {
		r.labStartupDuration.WithLabelValues(result, env).Observe(durationSeconds)
	}
}

// Collector는 테스트에서 testutil.CollectAndCount 등으로 직접 검증할 때만 사용
func (r *Recorder) Collector() *prometheus.CounterVec {
	return r.vmBootTotal
}

func (r *Recorder) LabStartCollector() *prometheus.CounterVec {
	return r.labStartTotal
}

func (r *Recorder) LabStartupDurationCollector() *prometheus.HistogramVec {
	return r.labStartupDuration
}
