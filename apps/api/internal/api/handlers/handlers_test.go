package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/api/handlers"
	"github.com/requset700k/cledyu/api/internal/config"
	"go.uber.org/zap"
)

func newTestConfig() *config.Config {
	return &config.Config{
		Server:      config.ServerConfig{Mode: "debug"},
		FrontendURL: "https://app.test",
		Keycloak: config.KeycloakConfig{
			URL:          "https://keycloak.test",
			Realm:        "test",
			ClientID:     "test-client",
			RedirectURI:  "https://api.test/api/v1/auth/callback",
			CookieDomain: ".test",
		},
	}
}

func newTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handlers.New(newTestConfig(), zap.NewNop(), nil, nil)

	r.GET("/health", h.Health)
	r.GET("/me", func(c *gin.Context) {
		c.Set("user_id", "test-user")
		c.Set("user_email", "test@cledyu.local")
		c.Set("user_name", "Test User")
		c.Set("user_role", "student")
		h.GetMe(c)
	})
	r.GET("/labs", h.ListLabs)
	r.GET("/labs/:id", h.GetLab)
	r.GET("/api/v1/auth/login", h.Login)
	return r
}

func TestHealth(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
}

func TestListLabs(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/labs", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["total"].(float64) == 0 {
		t.Error("expected at least one lab")
	}
}

func TestGetLab_Found(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/labs/lab-k8s-basics", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestGetLab_NotFound(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/labs/nonexistent", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGetMe(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != "test-user" {
		t.Errorf("expected id=test-user, got %v", body["id"])
	}
}

func TestLogin_SetsMockCookieAndRedirects(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasSuffix(loc, "/callback") {
		t.Errorf("expected redirect to /callback, got %s", loc)
	}
	cookies := w.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == "access_token" && c.Value == "mock-token" {
			found = true
		}
	}
	if !found {
		t.Error("expected access_token=mock-token cookie")
	}
}
