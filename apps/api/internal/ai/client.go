// Package ai는 AI 학습 도우미 BFF(apps/ai-tutor)의 HTTP 클라이언트다.
// 요청/응답 구조는 apps/ai-tutor/app/models.py 의 HintRequest/HintResponse 와 계약을 이룬다.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrUnavailable은 BFF가 503(ai_unavailable: 키 미설정/전 모델 티어 실패)을 반환했거나
// 네트워크 도달 불가일 때를 나타낸다. 호출자는 정적 hint_levels 로 폴백한다.
var ErrUnavailable = errors.New("ai tutor unavailable")

// ErrRateLimited는 BFF Guardrails 의 힌트 사용 한도(분당/세션당) 초과다.
// 폴백하지 않고 사용자에게 그대로 알린다(한도 우회 방지).
var ErrRateLimited = errors.New("ai hint rate limited")

// StepInfo는 힌트 대상 스텝 콘텐츠다(ai-tutor StepInfo 와 대응).
type StepInfo struct {
	ID          int      `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Commands    []string `json:"commands,omitempty"`
}

// HintRequest는 POST /v1/hints 요청 본문이다.
type HintRequest struct {
	UserID       string   `json:"user_id"`
	SessionID    string   `json:"session_id"`
	LabID        string   `json:"lab_id"`
	OrgID        string   `json:"org_id,omitempty"`
	Step         StepInfo `json:"step"`
	HintLevel    int      `json:"hint_level"`
	TerminalTail string   `json:"terminal_tail,omitempty"`
}

// DocSource는 힌트와 함께 표시할 관련 문서 링크(RAG 출처)다.
type DocSource struct {
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}

// HintResponse는 POST /v1/hints 응답 본문이다.
type HintResponse struct {
	Hint      string      `json:"hint"`
	HintLevel int         `json:"hint_level"`
	Model     string      `json:"model"`
	Sources   []DocSource `json:"sources,omitempty"`
}

// Client는 ai-tutor BFF 호출 클라이언트다. baseURL이 비면 nil 을 쓰는 대신
// 생성 자체를 건너뛴다(핸들러가 nil 체크 후 정적 폴백).
type Client struct {
	baseURL string
	http    *http.Client
}

// New는 BFF 클라이언트를 생성한다. baseURL이 비면 nil 을 반환한다.
func New(baseURL string, timeout time.Duration) *Client {
	if baseURL == "" {
		return nil
	}
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

// RequestHint는 BFF에 힌트 생성을 요청한다.
// 503 → ErrUnavailable(정적 폴백 신호), 429 → ErrRateLimited(사용자 노출).
func (c *Client) RequestHint(ctx context.Context, req HintRequest) (*HintResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal hint request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/hints", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build hint request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// 네트워크 단절/타임아웃도 미가용으로 본다 — 학생 UX 는 정적 힌트로 이어진다.
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		var out HintResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
			return nil, fmt.Errorf("%w: decode response: %s", ErrUnavailable, err)
		}
		return &out, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrRateLimited
	default:
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
}
