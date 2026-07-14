package ec2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// KeyMinter는 세션 EC2가 tailnet에 가입할 때 쓸 authkey를 발급한다.
// 세션마다 새 키를 발급해 user-data에 넣으므로, 정적 reusable 키가 세션 user-data(sudo lab 계정이
// 읽음)로 유출돼 세션 종료 후에도 외부 장치가 tag:lab-ec2로 등록되는 위험을 없앤다(issue #307).
type KeyMinter interface {
	// Mint는 비재사용·ephemeral·태그·짧은 만료의 tailnet authkey를 발급해 반환한다.
	Mint(ctx context.Context) (string, error)
}

// tailscaleKeyMinter는 Tailscale API(POST /api/v2/tailnet/{tailnet}/keys)로 세션 authkey를 발급한다.
// apiKey는 auth_keys write 스코프 + tag 발급 권한을 가진 API 액세스 토큰(OAuth client token 등)이다.
type tailscaleKeyMinter struct {
	httpc   *http.Client
	baseURL string // 기본 https://api.tailscale.com
	apiKey  string
	tailnet string // 기본 "-" (토큰 소유 tailnet)
	tag     string // 예: tag:lab-ec2
	ttl     time.Duration
}

func newTailscaleKeyMinter(apiKey, tailnet, tag string, ttl time.Duration) *tailscaleKeyMinter {
	if tailnet == "" {
		tailnet = "-"
	}
	return &tailscaleKeyMinter{
		httpc:   &http.Client{Timeout: 10 * time.Second},
		baseURL: "https://api.tailscale.com",
		apiKey:  apiKey,
		tailnet: tailnet,
		tag:     tag,
		ttl:     ttl,
	}
}

// createKeyRequest / createKeyResponse는 Tailscale API 스키마의 필요한 부분만 담는다.
type createKeyRequest struct {
	Capabilities struct {
		Devices struct {
			Create struct {
				Reusable      bool     `json:"reusable"`
				Ephemeral     bool     `json:"ephemeral"`
				Preauthorized bool     `json:"preauthorized"`
				Tags          []string `json:"tags"`
			} `json:"create"`
		} `json:"devices"`
	} `json:"capabilities"`
	ExpirySeconds int64  `json:"expirySeconds"`
	Description   string `json:"description,omitempty"`
}

type createKeyResponse struct {
	Key string `json:"key"`
}

func (m *tailscaleKeyMinter) Mint(ctx context.Context) (string, error) {
	var body createKeyRequest
	// 세션별 one-off: reusable=false(1회 소비), ephemeral=true(종료 시 자동 정리),
	// preauthorized=true(태그 노드라 사람 승인 없이 즉시 활성), 짧은 만료.
	body.Capabilities.Devices.Create.Reusable = false
	body.Capabilities.Devices.Create.Ephemeral = true
	body.Capabilities.Devices.Create.Preauthorized = true
	body.Capabilities.Devices.Create.Tags = []string{m.tag}
	body.ExpirySeconds = int64(m.ttl.Seconds())
	body.Description = "cledyu ec2 session (one-off)"

	buf, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("authkey: 요청 직렬화: %w", err)
	}

	url := fmt.Sprintf("%s/api/v2/tailnet/%s/keys", m.baseURL, m.tailnet)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("authkey: 요청 생성: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("authkey: 발급 요청: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("authkey: 발급 실패 status=%d body=%s", resp.StatusCode, string(rb))
	}
	var out createKeyResponse
	if err := json.Unmarshal(rb, &out); err != nil {
		return "", fmt.Errorf("authkey: 응답 파싱: %w", err)
	}
	if out.Key == "" {
		return "", fmt.Errorf("authkey: 응답에 key 없음")
	}
	return out.Key, nil
}
