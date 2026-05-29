package validation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidationRequestJSONContract(t *testing.T) {
	data, err := json.Marshal(ValidationRequest{
		TraceID:   "trace-1",
		SessionID: "session-1",
		StepID:    1,
		VM: VMSpec{
			Type:      VMTypeKubeVirt,
			Name:      "session-vm",
			Namespace: "lab-session-1",
		},
		Checks: []Check{
			{
				Type:    CheckCommand,
				Command: "stat -c %a /home/lab/work/notes.txt",
				Expect:  "600",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := string(data)
	if !strings.Contains(body, `"cmd":"stat -c %a /home/lab/work/notes.txt"`) {
		t.Fatalf("expected command field to marshal as cmd, got %s", body)
	}
	if strings.Contains(body, `"command":`) {
		t.Fatalf("unexpected command key in validation-engine contract: %s", body)
	}
}
