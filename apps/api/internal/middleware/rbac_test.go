package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// rbacRouter는 user_role 을 주입하는 스텁 + RequireMinRole 보호 라우트를 구성한다.
func rbacRouter(role, minRole string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) {
		if role != "" {
			c.Set("user_role", role)
		}
		c.Next()
	}
	r.GET("/x", inject, RequireMinRole(minRole), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func get(r *gin.Engine) int {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	return w.Code
}

func TestRequireMinRole(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		minRole string
		want    int
	}{
		{"admin meets admin", "admin", "admin", http.StatusOK},
		{"admin meets instructor (위계)", "admin", "instructor", http.StatusOK},
		{"instructor meets instructor", "instructor", "instructor", http.StatusOK},
		{"instructor denied admin", "instructor", "admin", http.StatusForbidden},
		{"student denied instructor", "student", "instructor", http.StatusForbidden},
		{"student meets student", "student", "student", http.StatusOK},
		{"missing role denied", "", "instructor", http.StatusForbidden},
		{"unknown role denied", "wizard", "admin", http.StatusForbidden},
		{"invalid minRole fails closed", "admin", "superadmin", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := get(rbacRouter(tc.role, tc.minRole)); got != tc.want {
				t.Errorf("role=%q min=%q: got %d, want %d", tc.role, tc.minRole, got, tc.want)
			}
		})
	}
}
