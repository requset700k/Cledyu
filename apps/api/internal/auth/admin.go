package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/requset700k/cledyu/api/internal/config"
	"golang.org/x/oauth2/clientcredentials"
)

// ErrRoleNotFound/ErrUserNotFound 은 핸들러가 404/400 로 구분 응답하기 위한 센티넬이다.
var (
	ErrRoleNotFound = errors.New("realm role not found")
	ErrUserNotFound = errors.New("keycloak user not found")
)

// AdminClient는 Keycloak Admin REST API 로 유저 역할을 관리한다(역할 승격).
// service-account(client_credentials) 토큰은 oauth2 가 자동 획득·캐시·갱신한다.
// 필요한 realm-management 역할: view-realm(역할 조회), manage-users(매핑 변경).
type AdminClient struct {
	base    string // 예: https://keycloak.cledyu.local
	realm   string
	timeout time.Duration
	tokens  *clientcredentials.Config
}

// NewAdminClient는 admin service-account 설정으로 클라이언트를 만든다.
// AdminClientID/Secret 둘 중 하나라도 비면 nil 을 반환한다(역할 승격 API 비활성).
func NewAdminClient(cfg config.KeycloakConfig) *AdminClient {
	if cfg.AdminClientID == "" || cfg.AdminClientSecret == "" {
		return nil
	}
	base := strings.TrimRight(cfg.URL, "/")
	return &AdminClient{
		base:    base,
		realm:   cfg.Realm,
		timeout: 10 * time.Second,
		tokens: &clientcredentials.Config{
			ClientID:     cfg.AdminClientID,
			ClientSecret: cfg.AdminClientSecret,
			TokenURL:     base + "/realms/" + cfg.Realm + "/protocol/openid-connect/token",
		},
	}
}

// realmRole은 Keycloak 역할 표현의 부분 집합이다.
type realmRole struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AssignRealmRole은 유저에게 realm 역할을 추가한다(멱등 — 이미 있으면 Keycloak 이 204).
// Role() 우선순위가 최고 역할을 택하므로 '추가'만으로 승격이 성립한다.
func (a *AdminClient) AssignRealmRole(ctx context.Context, userID, role string) error {
	rr, err := a.getRealmRole(ctx, role)
	if err != nil {
		return err
	}
	body, _ := json.Marshal([]realmRole{rr})
	path := fmt.Sprintf("/admin/realms/%s/users/%s/role-mappings/realm",
		url.PathEscape(a.realm), url.PathEscape(userID))
	return a.send(ctx, http.MethodPost, path, body, http.StatusNoContent)
}

// client는 service-account 토큰을 자동 주입하는 HTTP 클라이언트를 반환한다.
func (a *AdminClient) client(ctx context.Context) *http.Client {
	c := a.tokens.Client(ctx)
	c.Timeout = a.timeout
	return c
}

// getRealmRole은 역할 이름으로 표현(id 포함)을 조회한다.
func (a *AdminClient) getRealmRole(ctx context.Context, role string) (realmRole, error) {
	path := fmt.Sprintf("/admin/realms/%s/roles/%s", url.PathEscape(a.realm), url.PathEscape(role))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+path, nil)
	if err != nil {
		return realmRole{}, fmt.Errorf("build get-role request: %w", err)
	}
	resp, err := a.client(ctx).Do(req)
	if err != nil {
		return realmRole{}, fmt.Errorf("get realm role: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return realmRole{}, fmt.Errorf("%w: %q", ErrRoleNotFound, role)
	}
	if resp.StatusCode != http.StatusOK {
		return realmRole{}, fmt.Errorf("get realm role: status %d", resp.StatusCode)
	}
	var rr realmRole
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&rr); err != nil {
		return realmRole{}, fmt.Errorf("decode realm role: %w", err)
	}
	return rr, nil
}

// send는 본문 있는 admin 요청을 보내고 기대 상태코드를 확인한다.
func (a *AdminClient) send(ctx context.Context, method, path string, body []byte, want int) error {
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build admin request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client(ctx).Do(req)
	if err != nil {
		return fmt.Errorf("admin api %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrUserNotFound
	}
	if resp.StatusCode != want {
		return fmt.Errorf("admin api %s %s: status %d", method, path, resp.StatusCode)
	}
	return nil
}
