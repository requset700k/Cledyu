// Package events는 학습 이벤트를 Kafka lab-events 토픽으로 발행한다(기획서 3.4).
// 컨슈머는 학습 분석 파이프라인(Airflow → BigQuery, 후속 구축)이며, 이벤트 스키마가
// 곧 데이터 계약이다 — 필드 변경 시 하위호환을 검토할 것.
package events

import "time"

// Type은 학습 이벤트 종류다(기획서 3.4 학습 분석 파이프라인의 이벤트 목록).
type Type string

const (
	LabStarted       Type = "lab_started"
	StepCompleted    Type = "step_completed"
	ValidationFailed Type = "validation_failed"
	HintRequested    Type = "hint_requested"
	LabCompleted     Type = "lab_completed"
	LabAbandoned     Type = "lab_abandoned"
)

// Event는 lab-events 토픽의 메시지 본문이다.
// 파티션 키는 UserID — 같은 학습자의 이벤트 순서를 보존한다(topics/lab-events.yaml).
type Event struct {
	Type      Type   `json:"event_type"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	LabID     string `json:"lab_id"`
	// StepID: step_completed / validation_failed / hint_requested 에서 채워진다.
	StepID int `json:"step_id,omitempty"`
	// HintLevel·HintSource: hint_requested 전용 (source: ai | static).
	HintLevel  int    `json:"hint_level,omitempty"`
	HintSource string `json:"hint_source,omitempty"`
	// VMProvider: 온프렘/EC2 분포 집계용(vm_provisioned_source). lab_started 에서 채워진다.
	VMProvider string    `json:"vm_provisioned_source,omitempty"`
	Timestamp  time.Time `json:"ts"`
}
