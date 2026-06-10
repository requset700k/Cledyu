package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 역할 위계 — 숫자가 클수록 권한이 넓다. 상위 역할은 하위 역할 라우트를 모두 통과한다
// (admin 은 instructor 전용 라우트도 접근 가능). auth.Identity.Role() 의 우선순위와 정렬됨.
var roleRank = map[string]int{
	"student":    1,
	"instructor": 2,
	"admin":      3,
}

// RequireMinRole은 JWT 미들웨어 뒤에 적용해 user_role 이 minRole 이상인지 검사한다.
// 미만이면 403(forbidden)으로 중단한다. JWT 미들웨어가 먼저 신원을 검증·주입하므로
// 여기서는 인가(authorization)만 본다 — 토큰 부재/무효는 이미 401 로 걸러진 뒤다.
//
// minRole 이 알 수 없는 값이면(프로그래머 오류) 모든 요청을 막는다(fail-closed).
func RequireMinRole(minRole string) gin.HandlerFunc {
	min := roleRank[minRole]
	return func(c *gin.Context) {
		if min == 0 || roleRank[c.GetString("user_role")] < min {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "insufficient role",
				"code":  "forbidden",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
