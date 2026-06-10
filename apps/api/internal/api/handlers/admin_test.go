package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/middleware"
	"go.uber.org/zap"
)

// adminRouter는 role 을 주입하는 스텁 + RBAC 미들웨어 + admin 라우트를 구성한다
// (라우터 배선과 동일하게 RequireMinRole("admin")을 통과시킨다).
func adminRouter(h *Handler, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin/users",
		func(c *gin.Context) { c.Set("user_role", role); c.Next() },
		middleware.RequireMinRole("admin"),
		h.ListUsers,
	)
	return r
}

func TestListUsers_RBAC(t *testing.T) {
	db := newFakePersistence()
	_ = db.UpsertUser(context.Background(), "u1", "a@b.c", "Alice", "student")
	_ = db.UpsertUser(context.Background(), "u2", "c@d.e", "Bob", "instructor")
	h := &Handler{log: zap.NewNop(), db: db}

	// student/instructor 는 admin 라우트에서 403.
	for _, role := range []string{"student", "instructor"} {
		w := httptest.NewRecorder()
		adminRouter(h, role).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/users", nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("role=%s: expected 403, got %d", role, w.Code)
		}
	}

	// admin 은 통과하고 유저 목록을 받는다.
	w := httptest.NewRecorder()
	adminRouter(h, "admin").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/users", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("admin: expected 200, got %d", w.Code)
	}
	var body struct {
		Total int `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 {
		t.Errorf("expected 2 users, got %d", body.Total)
	}
}

// DB 미설정이면 admin 이어도 503(유저 미러는 DB 에만 존재).
func TestListUsers_NoDB(t *testing.T) {
	h := &Handler{log: zap.NewNop()}
	w := httptest.NewRecorder()
	adminRouter(h, "admin").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/users", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without db, got %d", w.Code)
	}
}
