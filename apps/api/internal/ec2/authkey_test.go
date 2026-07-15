package ec2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTailscaleKeyMinter_Mint(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody createKeyRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = io.WriteString(w, `{"key":"tskey-auth-minted123","id":"k1"}`)
	}))
	defer srv.Close()

	m := newTailscaleKeyMinter("", "tskey-api-xyz", "-", "tag:lab-ec2", 600*time.Second)
	m.baseURL = srv.URL

	key, err := m.Mint(context.Background())
	if err != nil {
		t.Fatalf("Mint 실패: %v", err)
	}
	if key != "tskey-auth-minted123" {
		t.Errorf("발급 key = %q, want tskey-auth-minted123", key)
	}
	// 세션별 one-off 계약: 재사용 금지 + ephemeral + preauthorized + 태그 + 만료.
	c := gotBody.Capabilities.Devices.Create
	if c.Reusable {
		t.Error("reusable 이 true — 세션 키는 반드시 비재사용(one-off)이어야 한다")
	}
	if !c.Ephemeral {
		t.Error("ephemeral 이 false — 종료 시 자동정리를 위해 true 여야 한다")
	}
	if !c.Preauthorized {
		t.Error("preauthorized 가 false — 태그 노드 즉시 활성 위해 true 여야 한다")
	}
	if len(c.Tags) != 1 || c.Tags[0] != "tag:lab-ec2" {
		t.Errorf("tags = %v, want [tag:lab-ec2]", c.Tags)
	}
	if gotBody.ExpirySeconds != 600 {
		t.Errorf("expirySeconds = %d, want 600", gotBody.ExpirySeconds)
	}
	// description 은 Tailscale 허용 문자(영숫자·공백·하이픈·밑줄)만 — 괄호 등은 실 API 가 400
	// "invalid characters" 로 거부해 발급이 통째로 실패한다(라이브 실측). 회귀 가드.
	if !regexp.MustCompile(`^[A-Za-z0-9 _-]*$`).MatchString(gotBody.Description) {
		t.Errorf("description 에 Tailscale 비허용 문자 포함: %q", gotBody.Description)
	}
	if gotAuth != "Bearer tskey-api-xyz" {
		t.Errorf("Authorization = %q, want Bearer tskey-api-xyz", gotAuth)
	}
	if gotPath != "/api/v2/tailnet/-/keys" {
		t.Errorf("path = %q, want /api/v2/tailnet/-/keys", gotPath)
	}
}

func TestTailscaleKeyMinter_Mint_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"forbidden"}`)
	}))
	defer srv.Close()

	m := newTailscaleKeyMinter("", "bad", "-", "tag:lab-ec2", 600*time.Second)
	m.baseURL = srv.URL

	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("403 에서 에러를 기대했으나 nil")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("에러에 status 미포함: %v", err)
	}
}

// clientID 설정 시 client_credentials 로 액세스 토큰을 먼저 교환하고, /keys 요청에는 정적 secret 이
// 아니라 교환된 액세스 토큰을 Bearer 로 실어야 한다(OAuth 액세스 토큰 1h 만료 대응 — issue #307/codex).
func TestTailscaleKeyMinter_Mint_OAuthExchange(t *testing.T) {
	var tokenForm url.Values
	var keysAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/oauth/token":
			_ = r.ParseForm()
			tokenForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"tskey-api-exchanged","token_type":"Bearer","expires_in":3600}`)
		case "/api/v2/tailnet/-/keys":
			keysAuth = r.Header.Get("Authorization")
			_, _ = io.WriteString(w, `{"key":"tskey-auth-oauth999","id":"k2"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	m := newTailscaleKeyMinterBase(srv.URL, "cid-123", "tskey-client-secret", "-", "tag:lab-ec2", 600*time.Second)

	key, err := m.Mint(context.Background())
	if err != nil {
		t.Fatalf("Mint(OAuth) 실패: %v", err)
	}
	if key != "tskey-auth-oauth999" {
		t.Errorf("발급 key = %q, want tskey-auth-oauth999", key)
	}
	// 토큰 교환에 client_id/secret 이 폼으로 전달됐는가.
	if got := tokenForm.Get("client_id"); got != "cid-123" {
		t.Errorf("token client_id = %q, want cid-123", got)
	}
	if got := tokenForm.Get("client_secret"); got != "tskey-client-secret" {
		t.Errorf("token client_secret = %q, want tskey-client-secret", got)
	}
	// /keys 요청은 정적 secret 이 아니라 교환된 액세스 토큰을 실어야 한다.
	if keysAuth != "Bearer tskey-api-exchanged" {
		t.Errorf("/keys Authorization = %q, want Bearer tskey-api-exchanged", keysAuth)
	}
}
