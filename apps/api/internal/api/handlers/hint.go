package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/ai"
	"github.com/requset700k/cledyu/api/internal/events"
	"go.uber.org/zap"
)

const maxHintLevel = 3

// RequestHint는 현재 스텝에 대한 소크라테스식 힌트를 반환한다.
// AI BFF(ai-tutor)가 설정돼 있으면 Gemini 힌트를, 미설정/미가용이면 Lab DSL 의
// 정적 hint_levels 를 반환한다(기획서 3.5 다층 fallback 의 최종 단계).
// 레벨을 명시하지 않으면 스텝별 사용 이력에 따라 1→2→3 으로 자동 상승한다.
// POST /api/v1/sessions/:id/hint  body: { "step_id": 1, "level": 2?, "terminal_tail": "..."? }
func (h *Handler) RequestHint(c *gin.Context) {
	var req struct {
		StepID int `json:"step_id" binding:"required,min=1"`
		// Level 0(미지정)이면 자동 상승. 명시 시 1~3.
		Level int `json:"level" binding:"omitempty,min=1,max=3"`
		// TerminalTail: 학생 터미널의 최근 출력(옵션) — '막힌 지점' 컨텍스트.
		TerminalTail string `json:"terminal_tail"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.err(c, http.StatusBadRequest, "step_id is required")
		return
	}
	// 터미널 컨텍스트 길이 상한 — 토큰 비용/프롬프트 폭주 방지(BFF 쪽 4000자 제한과 정렬).
	if len(req.TerminalTail) > 4000 {
		req.TerminalTail = req.TerminalTail[len(req.TerminalTail)-4000:]
	}

	sessionID := c.Param("id")
	if h.denyIfNotStoreOwner(c, sessionID) {
		return
	}
	labID, level, ok := h.steps.nextHint(sessionID, req.StepID, req.Level)
	if !ok {
		h.err(c, http.StatusNotFound, "session not found")
		return
	}

	lc, ok := h.labs[labID]
	if !ok {
		h.err(c, http.StatusNotFound, "lab content not found")
		return
	}
	step, ok := findContentStep(lc, req.StepID)
	if !ok {
		h.err(c, http.StatusNotFound, "step content not found")
		return
	}

	userID, _ := c.Get("user_id")
	uid, _ := userID.(string)
	// 소속 조직 collection — JWT 미들웨어가 Keycloak 그룹에서 도출(없으면 public).
	// ai-tutor 가 [org, public] 을 함께 검색한다(기획서 3.5 RAG 멀티테넌트).
	org := c.GetString("user_org")
	if org == "" {
		org = "public"
	}

	if h.ai != nil {
		resp, err := h.ai.RequestHint(c.Request.Context(), ai.HintRequest{
			UserID:    uid,
			SessionID: sessionID,
			LabID:     labID,
			OrgID:     org,
			Step: ai.StepInfo{
				ID:          step.ID,
				Title:       step.Title,
				Description: step.Description,
				Commands:    step.Commands,
			},
			HintLevel:    level,
			TerminalTail: req.TerminalTail,
		})
		switch {
		case err == nil:
			h.emitEvent(events.Event{
				Type: events.HintRequested, UserID: uid, SessionID: sessionID, LabID: labID,
				StepID: req.StepID, HintLevel: level, HintSource: "ai",
			})
			c.JSON(http.StatusOK, gin.H{
				"hint":       resp.Hint,
				"hint_level": resp.HintLevel,
				"source":     "ai",
				"model":      resp.Model,
				"sources":    resp.Sources,
			})
			return
		case errors.Is(err, ai.ErrRateLimited):
			// 한도 초과는 정적 폴백으로 우회시키지 않는다 — Guardrails 의 목적 유지.
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "힌트 사용 한도에 도달했습니다. 잠시 후 다시 시도하세요.",
				"code":  "hint_rate_limited",
			})
			return
		default:
			h.log.Warn("ai tutor 미가용 — 정적 힌트로 폴백", zap.Error(err),
				zap.String("session_id", sessionID), zap.Int("step_id", req.StepID))
		}
	}

	hint := step.StaticHint(level)
	if hint == "" {
		h.err(c, http.StatusNotFound, "no hint available for this step")
		return
	}
	h.emitEvent(events.Event{
		Type: events.HintRequested, UserID: uid, SessionID: sessionID, LabID: labID,
		StepID: req.StepID, HintLevel: level, HintSource: "static",
	})
	c.JSON(http.StatusOK, gin.H{
		"hint":       hint,
		"hint_level": level,
		"source":     "static",
	})
}

// nextHint는 (labID, 사용할 힌트 레벨)을 반환하고 스텝의 힌트 사용 이력을 갱신한다.
// explicit(1~3)이 지정되면 그대로 쓰고, 0이면 직전 사용 레벨+1(최대 3)로 자동 상승한다.
// 세션이 없으면 ok=false. 스텝 진행 엔트리가 없어도 세션만 있으면 동작한다(이력만 못 쌓음).
func (st *stepStore) nextHint(sessionID string, stepID, explicit int) (labID string, level int, ok bool) {
	ok = st.withSession(sessionID, func(ss *sessionSteps) bool {
		labID = ss.LabID
		level = explicit
		for i := range ss.Steps {
			if ss.Steps[i].StepID != stepID {
				continue
			}
			if level == 0 {
				level = ss.Steps[i].HintLevel + 1
				if level > maxHintLevel {
					level = maxHintLevel
				}
			}
			if level > ss.Steps[i].HintLevel {
				ss.Steps[i].HintLevel = level
				return true // 힌트 사용 이력 변경 — write-through
			}
			return false
		}
		if level == 0 {
			level = 1
		}
		return false
	})
	return labID, level, ok
}
