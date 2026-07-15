package ec2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// KeyMinter는 세션 EC2가 tailnet에 가입할 때 쓸 authkey를 발급한다.
// 세션마다 새 키를 발급해 user-data에 넣으므로, 정적 reusable 키가 세션 user-data(sudo lab 계정이
// 읽음)로 유출돼 세션 종료 후에도 외부 장치가 tag:lab-ec2로 등록되는 위험을 없앤다(issue #307).
type KeyMinter interface {
	// Mint는 비재사용·ephemeral·태그·짧은 만료의 tailnet authkey를 발급해 반환한다.
	Mint(ctx context.Context) (string, error)
}

// tailscaleKeyMinter는 Tailscale API(POST /api/v2/tailnet/{tailnet}/keys)로 세션 authkey를 발급한다.
//
// 인증은 두 방식을 지원한다:
//   - clientID 설정: apiKey 를 OAuth **client secret** 으로 보고 client_credentials 로 액세스 토큰을
//     교환(자동 갱신)한다. httpc(oauth2 client)가 매 요청에 유효 토큰을 주입한다. OAuth 액세스 토큰은
//     1시간 만료라 정적 baked 는 불가하므로 지속 동작하려면 이 교환 방식이어야 한다.
//   - clientID 미설정: apiKey 를 API 액세스 토큰으로 보고 직접 Bearer 로 보낸다(하위호환).
type tailscaleKeyMinter struct {
	httpc    *http.Client
	baseURL  string // 기본 https://api.tailscale.com
	apiKey   string // useOAuth=false 일 때만 직접 Bearer 로 사용
	useOAuth bool   // true면 httpc 가 OAuth 토큰을 주입/갱신(수동 Authorization 헤더 생략)
	tailnet  string // 기본 "-" (토큰 소유 tailnet)
	tag      string // 예: tag:lab-ec2
	ttl      time.Duration
}

func newTailscaleKeyMinter(clientID, apiKey, tailnet, tag string, ttl time.Duration) *tailscaleKeyMinter {
	return newTailscaleKeyMinterBase("https://api.tailscale.com", clientID, apiKey, tailnet, tag, ttl)
}

// newTailscaleKeyMinterBase는 baseURL 을 주입받는다(httptest 로 /keys·/oauth/token 을 함께 스텁).
func newTailscaleKeyMinterBase(baseURL, clientID, apiKey, tailnet, tag string, ttl time.Duration) *tailscaleKeyMinter {
	if tailnet == "" {
		tailnet = "-"
	}
	m := &tailscaleKeyMinter{
		baseURL: baseURL,
		apiKey:  apiKey,
		tailnet: tailnet,
		tag:     tag,
		ttl:     ttl,
	}
	if clientID != "" {
		// client_credentials: client_id/secret 을 폼 파라미터로 보내 액세스 토큰을 교환한다(Tailscale
		// 문서 curl 과 동일). 반환 클라이언트가 토큰을 캐시하고 만료(1h) 시 자동 갱신한다.
		cc := &clientcredentials.Config{
			ClientID:     clientID,
			ClientSecret: apiKey,
			TokenURL:     baseURL + "/api/v2/oauth/token",
			AuthStyle:    oauth2.AuthStyleInParams,
		}
		m.httpc = cc.Client(context.Background())
		m.httpc.Timeout = 10 * time.Second
		m.useOAuth = true
	} else {
		m.httpc = &http.Client{Timeout: 10 * time.Second}
	}
	return m
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
	// 설명은 Tailscale 키 description 허용 문자만 쓴다 — 괄호 등은 400 "invalid characters" 로
	// 거부돼 발급 자체가 실패한다(라이브 API 실측). 영숫자·공백·하이픈으로 제한.
	body.Description = "cledyu ec2 session one-off"

	buf, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("authkey: 요청 직렬화: %w", err)
	}

	url := fmt.Sprintf("%s/api/v2/tailnet/%s/keys", m.baseURL, m.tailnet)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("authkey: 요청 생성: %w", err)
	}
	// useOAuth 면 httpc(oauth2 client)가 유효 액세스 토큰을 자동 주입하므로 수동 헤더를 붙이지 않는다.
	if !m.useOAuth {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
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
