package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/session"
	"go.uber.org/zap"
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

// ensureStepSession은 진행 저장소에서 세션을 찾지 못했지만 VM 프로바이더에는 세션이
// 남아 있는 경우 최소 진행 상태를 복구한다. DB 없는 로컬 API를 재시작하면 in-memory
// stepStore만 사라지고 KubeVirt/EC2 세션은 계속 살아 있어 재접속 후 steps/validate/hint가
// 모두 404가 되는 경로를 복구하기 위한 것이다.
//
// 프로바이더가 가진 소유자와 Lab 메타데이터를 먼저 검증한 뒤 저장하므로, 세션 ID만 아는
// 다른 사용자가 복구 경로를 통해 타인의 세션을 자신의 진행 상태로 등록할 수 없다.
func (h *Handler) ensureStepSession(c *gin.Context, sessionID string) bool {
	if _, ok := h.steps.storeOwner(sessionID); ok {
		return true
	}
	// 영속 DB가 설정된 환경에서 진행 행이 사라졌다면 데이터 정합성 장애다. 살아 있는 VM만
	// 근거로 초기 상태를 덮어쓰면 사용자의 실제 통과 이력을 잃을 수 있으므로 로컬 in-memory
	// 모드에서만 복구한다. 운영 요청에서 임의 세션 ID마다 프로바이더 API를 조회하는 것도 막는다.
	if h.steps.db != nil {
		h.err(c, http.StatusNotFound, "session not found")
		return false
	}
	if h.sessions == nil {
		h.err(c, http.StatusNotFound, "session not found")
		return false
	}

	sess, err := h.sessions.Get(c.Request.Context(), sessionID)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			h.err(c, http.StatusNotFound, "session not found")
			return false
		}
		h.log.Error("recover session progress", zap.String("session_id", sessionID), zap.Error(err))
		h.err(c, http.StatusInternalServerError, "get session failed")
		return false
	}
	if h.denyIfNotSessionOwner(c, sess) {
		return false
	}

	lab, ok := h.labs[sess.LabID]
	if !ok {
		h.err(c, http.StatusNotFound, "lab content not found")
		return false
	}

	ss := &sessionSteps{LabID: sess.LabID, UserID: sess.UserID}
	for i, step := range lab.Steps {
		status := "pending"
		if i == 0 {
			status = "active"
		}
		ss.Steps = append(ss.Steps, stepState{StepID: step.ID, Status: status})
	}
	if len(ss.Steps) > 0 {
		ss.CurrentStep = ss.Steps[0].StepID
	}
	if err := h.steps.put(sessionID, ss); err != nil {
		h.err(c, http.StatusInternalServerError, "initialize session progress failed")
		return false
	}

	h.log.Warn("VM 세션에서 진행 상태를 복구했습니다",
		zap.String("session_id", sessionID), zap.String("lab_id", sess.LabID))
	return true
}
