package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/requset700k/cledyu/api/internal/config"
)

// fakeKeycloak는 token + roles + role-mappings 엔드포인트를 흉내 낸다.
type fakeKeycloak struct {
	server      *httptest.Server
	knownRoles  map[string]string // roleName → id
	knownUsers  map[string]bool
	mappingCall int
	lastMapping []realmRole
}

func newFakeKeycloak() *fakeKeycloak {
	f := &fakeKeycloak{
		knownRoles: map[string]string{"instructor": "role-uuid-1"},
		knownUsers: map[string]bool{"u1": true},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/test/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-sa-token", "token_type": "Bearer", "expires_in": 300,
		})
	})
	// GET /admin/realms/test/roles/{name}
	mux.HandleFunc("/admin/realms/test/roles/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[len("/admin/realms/test/roles/"):]
		id, ok := f.knownRoles[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(realmRole{ID: id, Name: name})
	})
	// POST /admin/realms/test/users/{id}/role-mappings/realm
	mux.HandleFunc("/admin/realms/test/users/", func(w http.ResponseWriter, r *http.Request) {
		// .../users/{id}/role-mappings/realm — id 추출
		const prefix = "/admin/realms/test/users/"
		rest := r.URL.Path[len(prefix):]
		uid := rest
		if i := indexByte(rest, '/'); i >= 0 {
			uid = rest[:i]
		}
		if !f.knownUsers[uid] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&f.lastMapping)
		f.mappingCall++
		w.WriteHeader(http.StatusNoContent)
	})
	f.server = httptest.NewServer(mux)
	return f
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func newTestAdminClient(f *fakeKeycloak) *AdminClient {
	return NewAdminClient(config.KeycloakConfig{
		URL: f.server.URL, Realm: "test",
		AdminClientID: "admin-sa", AdminClientSecret: "secret",
	})
}

// admin client 미설정(ID/Secret 빈 값)이면 nil.
func TestNewAdminClient_Disabled(t *testing.T) {
	if NewAdminClient(config.KeycloakConfig{URL: "x", Realm: "test"}) != nil {
		t.Fatal("expected nil admin client when credentials absent")
	}
}

// 정상 역할 부여 — role 조회 → role-mapping POST.
func TestAssignRealmRole_OK(t *testing.T) {
	f := newFakeKeycloak()
	defer f.server.Close()
	ac := newTestAdminClient(f)

	if err := ac.AssignRealmRole(context.Background(), "u1", "instructor"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.mappingCall != 1 || len(f.lastMapping) != 1 || f.lastMapping[0].Name != "instructor" {
		t.Errorf("expected one instructor mapping, got call=%d mapping=%+v", f.mappingCall, f.lastMapping)
	}
}

// realm 에 역할이 없으면 ErrRoleNotFound.
func TestAssignRealmRole_RoleNotFound(t *testing.T) {
	f := newFakeKeycloak()
	defer f.server.Close()
	ac := newTestAdminClient(f)

	err := ac.AssignRealmRole(context.Background(), "u1", "admin") // knownRoles 에 없음
	if !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("expected ErrRoleNotFound, got %v", err)
	}
}

// 존재하지 않는 유저면 ErrUserNotFound.
func TestAssignRealmRole_UserNotFound(t *testing.T) {
	f := newFakeKeycloak()
	defer f.server.Close()
	ac := newTestAdminClient(f)

	err := ac.AssignRealmRole(context.Background(), "ghost", "instructor")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}
