package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/session"
)

// 세션 소유자 검증 — 세션 ID 만 알면 타인의 세션(터미널 포함)에 접근할 수 있던
// 구멍을 막는다. 정책은 IDE 프록시(ide 핸들러)와 동일:
//   - 요청 신원(uid)과 세션 소유자가 모두 있고 서로 다르면 404 (존재 여부 비노출)
//   - 레거시 무소유 세션(UserID 빈 값)·신원 없는 요청(빈 uid)은 허용 — 로컬/디버그 경로
//
// 강사 관전 모드 도입 시 instructor/admin 역할 예외를 이 지점에 추가한다.

// requestUID는 JWT 미들웨어가 컨텍스트에 넣은 사용자 식별자(sub)를 반환한다.
func requestUID(c *gin.Context) string {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(string)
	return uid
}

// denyIfNotOwner는 소유자 불일치 시 404 를 쓰고 true 를 반환한다(호출자는 즉시 return).
func (h *Handler) denyIfNotOwner(c *gin.Context, ownerUID string) bool {
	uid := requestUID(c)
	if uid != "" && ownerUID != "" && uid != ownerUID {
		h.err(c, http.StatusNotFound, "session not found")
		return true
	}
	return false
}

// denyIfNotSessionOwner는 세션(프로바이더가 영속화한 소유자 메타) 소유자를 검사한다.
func (h *Handler) denyIfNotSessionOwner(c *gin.Context, sess *session.Session) bool {
	return h.denyIfNotOwner(c, sess.UserID)
}

// storeOwner는 stepStore 에 기록된 세션 소유자를 반환한다(세션 미존재 시 ok=false).
// withSession 경유라 캐시 미스 시 DB 적재(load-on-miss)도 함께 동작한다.
func (st *stepStore) storeOwner(sessionID string) (owner string, ok bool) {
	ok = st.withSession(sessionID, func(ss *sessionSteps) bool {
		owner = ss.UserID
		return false
	})
	return owner, ok
}

// denyIfNotStoreOwner는 stepStore 기반 핸들러(steps/validate/hint)의 소유자 검사다.
// 세션이 store 에 없으면 검사 없이 false 를 반환한다 — 이어지는 조회가 404 를 처리한다.
func (h *Handler) denyIfNotStoreOwner(c *gin.Context, sessionID string) bool {
	owner, ok := h.steps.storeOwner(sessionID)
	if !ok {
		return false
	}
	return h.denyIfNotOwner(c, owner)
}
