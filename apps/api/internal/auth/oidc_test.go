package auth

import (
	"net/url"
	"testing"

	"golang.org/x/oauth2"
)

func TestIdentityRole_Priority(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		want  string
	}{
		{"no roles defaults to student", nil, "student"},
		{"student only", []string{"student"}, "student"},
		{"instructor over student", []string{"student", "instructor"}, "instructor"},
		{"admin over instructor", []string{"student", "instructor", "admin"}, "admin"},
		{"unknown roles default student", []string{"offline_access", "uma_authorization"}, "student"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := Identity{Roles: tc.roles}
			if got := id.Role(); got != tc.want {
				t.Errorf("Role() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIdentityOrg(t *testing.T) {
	cases := []struct {
		name   string
		groups []string
		want   string
	}{
		{"no groups → public", nil, "public"},
		{"non-org groups → public", []string{"/instructors", "/staff"}, "public"},
		{"org group with leading slash", []string{"/org-kt-cloud"}, "org-kt-cloud"},
		{"org group without slash", []string{"org-company-x"}, "org-company-x"},
		{"first org group wins", []string{"/instructors", "/org-a", "/org-b"}, "org-a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := Identity{Groups: tc.groups}
			if got := id.Org(); got != tc.want {
				t.Errorf("Org() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaimsIdentity_NameFallsBackToUsername(t *testing.T) {
	c := claims{Subject: "abc", Email: "a@b.c", PreferredUsername: "kylekim"}
	id := c.identity()
	if id.Name != "kylekim" {
		t.Errorf("expected name to fall back to preferred_username, got %q", id.Name)
	}
	if id.Subject != "abc" || id.Email != "a@b.c" {
		t.Errorf("unexpected identity: %+v", id)
	}
}

func testProvider() *Provider {
	return &Provider{
		oauth2: oauth2.Config{
			ClientID:    "web",
			RedirectURL: "https://api.cledyu.local/api/v1/auth/callback",
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://keycloak.cledyu.local/realms/cledyu-learn/protocol/openid-connect/auth",
				TokenURL: "https://keycloak.cledyu.local/realms/cledyu-learn/protocol/openid-connect/token",
			},
			Scopes: []string{"openid", "profile", "email"},
		},
	}
}

func TestAuthCodeURL_IdPHint(t *testing.T) {
	p := testProvider()

	got := p.AuthCodeURL("st", "no", "verifier", false, "google")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Query().Get("kc_idp_hint") != "google" {
		t.Errorf("kc_idp_hint = %q, want google", u.Query().Get("kc_idp_hint"))
	}
}

func TestAuthCodeURL_NoHint_WhenEmpty(t *testing.T) {
	p := testProvider()

	got := p.AuthCodeURL("st", "no", "verifier", false, "")
	u, _ := url.Parse(got)
	if u.Query().Has("kc_idp_hint") {
		t.Errorf("kc_idp_hint must be absent, got %q", u.Query().Get("kc_idp_hint"))
	}
}
