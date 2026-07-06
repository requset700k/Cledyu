package vmmetrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewInitializesBootCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(reg)

	const want = `
# HELP vm_boot_total VM 부팅 결과 카운터(result=success|failed, env=kubevirt|ec2).
# TYPE vm_boot_total counter
vm_boot_total{env="ec2",result="failed"} 0
vm_boot_total{env="ec2",result="success"} 0
vm_boot_total{env="kubevirt",result="failed"} 0
vm_boot_total{env="kubevirt",result="success"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "vm_boot_total"); err != nil {
		t.Fatal(err)
	}
}

func TestNewInitializesLabStartMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(reg)

	const want = `
# HELP lab_start_total Lab 시작 결과 카운터(result=success|failed, reason=ready|vm_failed|timeout, env=onprem|ec2).
# TYPE lab_start_total counter
lab_start_total{env="ec2",reason="ready",result="success"} 0
lab_start_total{env="ec2",reason="timeout",result="failed"} 0
lab_start_total{env="ec2",reason="vm_failed",result="failed"} 0
lab_start_total{env="onprem",reason="ready",result="success"} 0
lab_start_total{env="onprem",reason="timeout",result="failed"} 0
lab_start_total{env="onprem",reason="vm_failed",result="failed"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "lab_start_total"); err != nil {
		t.Fatal(err)
	}
}
