package auth

import "testing"

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
