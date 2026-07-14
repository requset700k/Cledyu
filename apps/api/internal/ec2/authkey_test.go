package ec2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

	m := newTailscaleKeyMinter("tskey-api-xyz", "-", "tag:lab-ec2", 600*time.Second)
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

	m := newTailscaleKeyMinter("bad", "-", "tag:lab-ec2", 600*time.Second)
	m.baseURL = srv.URL

	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("403 에서 에러를 기대했으나 nil")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("에러에 status 미포함: %v", err)
	}
}
