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
