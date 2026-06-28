// Package bq는 D3 강사 분석용 읽기전용 BigQuery 클라이언트다.
// D2 가 만든 cledyu_analytics 뷰를 조회해 클린 DTO 로 반환한다.
// NULL(예: SAFE_DIVIDE 의 completion_rate)은 여기서 zero-value 로 흡수해
// 핸들러/JSON 은 nullable 타입을 다루지 않는다.
package bq

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

// LabCompletionRow는 v_lab_completion 한 행이다.
type LabCompletionRow struct {
	LabID          string  `json:"lab_id"`
	Started        int64   `json:"started"`
	Completed      int64   `json:"completed"`
	CompletionRate float64 `json:"completion_rate"`
}

// StepFunnelRow는 v_step_funnel 한 행이다(스텝별 검증 실패).
type StepFunnelRow struct {
	LabID              string `json:"lab_id"`
	StepID             int64  `json:"step_id"`
	ValidationFailures int64  `json:"validation_failures"`
}

// HintUsageRow는 v_hint_usage 한 행이다.
type HintUsageRow struct {
	LabID      string `json:"lab_id"`
	StepID     int64  `json:"step_id"`
	HintSource string `json:"hint_source"`
	HintCount  int64  `json:"hint_count"`
}

// Client는 cledyu_analytics 데이터셋을 조회하는 읽기전용 클라이언트다.
type Client struct {
	bq      *bigquery.Client
	dataset string
}

// NewClient는 ADC(GOOGLE_APPLICATION_CREDENTIALS)로 BigQuery 클라이언트를 만든다.
func NewClient(ctx context.Context, projectID, dataset string) (*Client, error) {
	c, err := bigquery.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("bigquery client: %w", err)
	}
	return &Client{bq: c, dataset: dataset}, nil
}

func (c *Client) Close() { _ = c.bq.Close() }

// LabCompletion은 v_lab_completion 을 조회한다. NULL completion_rate 는 0 으로 흡수.
func (c *Client) LabCompletion(ctx context.Context) ([]LabCompletionRow, error) {
	q := c.bq.Query(fmt.Sprintf(
		"SELECT lab_id, started, completed, completion_rate FROM `%s.v_lab_completion`", c.dataset))
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("query v_lab_completion: %w", err)
	}
	out := make([]LabCompletionRow, 0)
	for {
		var raw struct {
			LabID          bigquery.NullString
			Started        bigquery.NullInt64
			Completed      bigquery.NullInt64
			CompletionRate bigquery.NullFloat64
		}
		err := it.Next(&raw)
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("scan v_lab_completion: %w", err)
		}
		out = append(out, LabCompletionRow{
			LabID:          raw.LabID.StringVal,
			Started:        raw.Started.Int64,
			Completed:      raw.Completed.Int64,
			CompletionRate: raw.CompletionRate.Float64,
		})
	}
	return out, nil
}

// StepFunnel은 v_step_funnel 을 조회한다.
func (c *Client) StepFunnel(ctx context.Context) ([]StepFunnelRow, error) {
	q := c.bq.Query(fmt.Sprintf(
		"SELECT lab_id, step_id, validation_failures FROM `%s.v_step_funnel`", c.dataset))
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("query v_step_funnel: %w", err)
	}
	out := make([]StepFunnelRow, 0)
	for {
		var raw struct {
			LabID              bigquery.NullString
			StepID             bigquery.NullInt64
			ValidationFailures bigquery.NullInt64
		}
		err := it.Next(&raw)
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("scan v_step_funnel: %w", err)
		}
		out = append(out, StepFunnelRow{
			LabID:              raw.LabID.StringVal,
			StepID:             raw.StepID.Int64,
			ValidationFailures: raw.ValidationFailures.Int64,
		})
	}
	return out, nil
}

// HintUsage는 v_hint_usage 를 조회한다.
func (c *Client) HintUsage(ctx context.Context) ([]HintUsageRow, error) {
	q := c.bq.Query(fmt.Sprintf(
		"SELECT lab_id, step_id, hint_source, hint_count FROM `%s.v_hint_usage`", c.dataset))
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("query v_hint_usage: %w", err)
	}
	out := make([]HintUsageRow, 0)
	for {
		var raw struct {
			LabID      bigquery.NullString
			StepID     bigquery.NullInt64
			HintSource bigquery.NullString
			HintCount  bigquery.NullInt64
		}
		err := it.Next(&raw)
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("scan v_hint_usage: %w", err)
		}
		out = append(out, HintUsageRow{
			LabID:      raw.LabID.StringVal,
			StepID:     raw.StepID.Int64,
			HintSource: raw.HintSource.StringVal,
			HintCount:  raw.HintCount.Int64,
		})
	}
	return out, nil
}
