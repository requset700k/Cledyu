package validation

import (
	"encoding/json"
	"testing"
)

// 검증엔진(validation-results)이 내보내는 JSON을 그대로 ValidationResult로 역직렬화할 수 있는지
// (필드 태그 일치) 검증한다 — 엔진 model.ValidationResult와의 스키마 드리프트를 잡는 가드.
func TestValidationResultUnmarshal(t *testing.T) {
	payload := `{
		"trace_id": "abc123",
		"session_id": "sess-1",
		"step_id": 3,
		"passed": false,
		"checks": [
			{"type": "command", "passed": false, "detail": "expected 600 got 644"},
			{"type": "file_exists", "passed": true}
		],
		"duration_ms": 1820
	}`

	var r ValidationResult
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if r.SessionID != "sess-1" || r.StepID != 3 {
		t.Errorf("correlation keys wrong: session=%q step=%d", r.SessionID, r.StepID)
	}
	if r.Passed {
		t.Error("passed should be false")
	}
	if len(r.Checks) != 2 {
		t.Fatalf("checks len = %d, want 2", len(r.Checks))
	}
	if r.Checks[0].Type != CheckCommand || r.Checks[0].Passed || r.Checks[0].Detail == "" {
		t.Errorf("check[0] wrong: %+v", r.Checks[0])
	}
	if r.Checks[1].Type != CheckFileExists || !r.Checks[1].Passed {
		t.Errorf("check[1] wrong: %+v", r.Checks[1])
	}
	if r.DurationMS != 1820 {
		t.Errorf("duration = %d, want 1820", r.DurationMS)
	}
}
