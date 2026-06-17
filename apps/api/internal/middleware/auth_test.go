package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// access_token 쿠키는 실제 로그인 세션의 신원이다. 로컬 dev 환경은 Authorization 헤더에
// 항상 stub 값("dev-token")을 붙이는데(apps/web/lib/api.ts DEV_HEADERS), 헤더가 쿠키보다
// 우선되면 실제 로그인 후에도 매 요청이 stub 토큰으로 검증에 실패해 /login으로 되돌아간다.
// extractToken은 쿠키를 우선해야 한다.
func TestExtractToken_CookieTakesPriorityOverAuthorizationHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer dev-token")
	req.AddCookie(&http.Cookie{Name: "access_token", Value: "real-jwt"})

	c := &gin.Context{Request: req}

	if got, want := extractToken(c), "real-jwt"; got != want {
		t.Fatalf("extractToken() = %q, want %q (cookie should win over stale Authorization header)", got, want)
	}
}

func TestExtractToken_FallsBackToAuthorizationHeaderWithoutCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer real-jwt")

	c := &gin.Context{Request: req}

	if got, want := extractToken(c), "real-jwt"; got != want {
		t.Fatalf("extractToken() = %q, want %q", got, want)
	}
}
