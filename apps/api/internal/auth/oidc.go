// Package auth는 Keycloak OIDC 연동을 래핑한다 — authorization code(PKCE) 흐름과
// 토큰 검증. 핸들러(handlers.auth)와 미들웨어(middleware.JWT)가 *Provider를 공유한다.
package auth

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/requset700k/cledyu/api/internal/config"
	"golang.org/x/oauth2"
)

// Provider는 Keycloak realm 하나에 대한 OIDC discovery 결과와 oauth2 설정을 보관한다.
type Provider struct {
	oauth2         oauth2.Config
	idVerifier     *oidc.IDTokenVerifier // id_token 검증 (aud == client_id, nonce 확인)
	accessVerifier *oidc.IDTokenVerifier // access_token 검증 (aud 검사 생략 — Keycloak access token aud는 보통 "account")
	provider       *oidc.Provider
	endSessionURL  string
}

// Identity는 검증된 토큰에서 추출한 사용자 신원이다.
type Identity struct {
	Subject string
	Email   string
	Name    string
	Roles   []string
	Groups  []string
}

// claims는 Keycloak 토큰에서 읽는 표준/realm 클레임이다.
type claims struct {
	Subject           string   `json:"sub"`
	Email             string   `json:"email"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Groups            []string `json:"groups"`
	RealmAccess       struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (c claims) identity() *Identity {
	name := c.Name
	if name == "" {
		name = c.PreferredUsername
	}
	return &Identity{
		Subject: c.Subject,
		Email:   c.Email,
		Name:    name,
		Roles:   c.RealmAccess.Roles,
		Groups:  c.Groups,
	}
}

// orgGroupPrefix는 조직 소속을 나타내는 Keycloak 그룹 이름 규약이다.
// 예: 그룹 "/org-kt-cloud" → 조직 "org-kt-cloud". RAG 멀티테넌트(기획서 3.5)의
// 조직별 ChromaDB collection 이름과 1:1 대응한다.
const orgGroupPrefix = "org-"

// Org는 사용자가 속한 조직 collection 이름을 돌려준다(없으면 "public").
// Keycloak groups 클레임에서 "org-" 로 시작하는 첫 그룹을 채택한다. 그룹 경로의
// 앞쪽 "/"(예: "/org-kt-cloud")는 무시한다. 다중 org 그룹은 정렬상 첫 항목을 쓴다.
func (id Identity) Org() string {
	for _, g := range id.Groups {
		name := strings.TrimPrefix(g, "/")
		if strings.HasPrefix(name, orgGroupPrefix) {
			return name
		}
	}
	return "public"
}

// Role은 역할 우선순위(admin > instructor > student)에 따라 단일 역할을 돌려준다.
// 학습자 realm 신규 가입자는 student 만 가지므로 기본값은 student.
func (id Identity) Role() string {
	has := func(want string) bool {
		for _, r := range id.Roles {
			if r == want {
				return true
			}
		}
		return false
	}
	switch {
	case has("admin"):
		return "admin"
	case has("instructor"):
		return "instructor"
	default:
		return "student"
	}
}

// NewProvider는 issuer discovery(.well-known)를 수행한다. Keycloak 미가용(CI/로컬)
// 환경에서는 에러를 돌려주며, 호출부는 nil Provider로 graceful degradation 한다.
func NewProvider(ctx context.Context, cfg config.KeycloakConfig) (*Provider, error) {
	issuer := strings.TrimRight(cfg.URL, "/") + "/realms/" + cfg.Realm

	op, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery (%s): %w", issuer, err)
	}

	var endpoints struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = op.Claims(&endpoints)

	return &Provider{
		oauth2: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     op.Endpoint(),
			RedirectURL:  cfg.RedirectURI,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		idVerifier:     op.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		accessVerifier: op.Verifier(&oidc.Config{SkipClientIDCheck: true}),
		provider:       op,
		endSessionURL:  endpoints.EndSession,
	}, nil
}

// AuthCodeURL은 state/nonce/PKCE(S256) 를 적용한 Keycloak 인가 URL을 만든다.
// register=true 면 로그인 대신 회원가입 폼으로 딥링크한다(Keycloak 의 /registrations
// 엔드포인트). idp != "" 면 kc_idp_hint 를 붙여 해당 IdP 로 직행한다 —
// OIDC 흐름(state/PKCE/redirect)은 그대로 유지된다.
func (p *Provider) AuthCodeURL(state, nonce, pkceVerifier string, register bool, idp string) string {
	opts := []oauth2.AuthCodeOption{
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(pkceVerifier),
	}
	if idp != "" {
		opts = append(opts, oauth2.SetAuthURLParam("kc_idp_hint", idp))
	}
	u := p.oauth2.AuthCodeURL(state, opts...)
	if register {
		u = strings.Replace(u, "/protocol/openid-connect/auth", "/protocol/openid-connect/registrations", 1)
	}
	return u
}

// Exchange는 authorization code를 토큰으로 교환한다(PKCE verifier 포함).
func (p *Provider) Exchange(ctx context.Context, code, pkceVerifier string) (*oauth2.Token, error) {
	return p.oauth2.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
}

// Refresh는 refresh_token 으로 새 토큰 세트를 발급받는다(refresh_token grant).
// Keycloak 은 기본 설정에서 회전된 refresh_token 을 함께 돌려주므로, 호출부는 응답의
// RefreshToken 으로 쿠키를 갱신해야 한다. 만료/폐기된 refresh_token 은 에러를 반환한다.
func (p *Provider) Refresh(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	tok, err := p.oauth2.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token grant: %w", err)
	}
	return tok, nil
}

// VerifyIDToken은 토큰 응답의 id_token을 검증하고 nonce 일치를 확인한다.
func (p *Provider) VerifyIDToken(ctx context.Context, token *oauth2.Token, wantNonce string) (*Identity, error) {
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, fmt.Errorf("token response missing id_token")
	}
	idt, err := p.idVerifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}
	if idt.Nonce != wantNonce {
		return nil, fmt.Errorf("nonce mismatch")
	}
	var c claims
	if err := idt.Claims(&c); err != nil {
		return nil, fmt.Errorf("parse id_token claims: %w", err)
	}
	return c.identity(), nil
}

// VerifyAccessToken은 보호된 요청의 access_token(쿠키/Bearer/쿼리)을 검증한다.
func (p *Provider) VerifyAccessToken(ctx context.Context, raw string) (*Identity, error) {
	tok, err := p.accessVerifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("verify access_token: %w", err)
	}
	var c claims
	if err := tok.Claims(&c); err != nil {
		return nil, fmt.Errorf("parse access_token claims: %w", err)
	}
	return c.identity(), nil
}

// LogoutURL은 Keycloak end-session 엔드포인트 URL을 만든다(없으면 빈 문자열).
func (p *Provider) LogoutURL(idTokenHint, postLogoutRedirect string) string {
	if p.endSessionURL == "" {
		return ""
	}
	q := url.Values{}
	q.Set("client_id", p.oauth2.ClientID)
	if postLogoutRedirect != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirect)
	}
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}
	return p.endSessionURL + "?" + q.Encode()
}
