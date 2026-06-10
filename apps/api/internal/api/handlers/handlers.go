// Package handlers는 도메인별 HTTP 핸들러를 구현한다 (health, lab, user, auth).
package handlers

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/ai"
	"github.com/requset700k/cledyu/api/internal/auth"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/events"
	"github.com/requset700k/cledyu/api/internal/kube"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"github.com/requset700k/cledyu/api/internal/validation"
	"go.uber.org/zap"
	"kubevirt.io/client-go/kubecli"
)

// Handler는 모든 HTTP 핸들러의 공유 의존성을 보관한다.
type Handler struct {
	cfg       *config.Config
	log       *zap.Logger
	auth      *auth.Provider                // Keycloak OIDC. nil 허용 — discovery 실패(CI/로컬) 시 인증 흐름 비활성.
	labs      map[string]content.LabContent // lab id → DSL 콘텐츠(스텝). GetLab/세션에서 사용.
	sessions  *kubevirt.Manager             // KubeVirt VM 수명주기. nil 허용 — 클러스터 미연결 시 세션 API 503.
	steps     *stepStore                    // STUB(검증엔진 연동 전): 세션별 스텝 진행 상태 in-memory.
	virt      kubecli.KubevirtClient        // VM serial console 접속용 KubeVirt 클라이언트. nil이면 콘솔 비활성.
	validator validation.Publisher          // validation-requests Kafka 발행기. nil이면 debug 모드에서 mock 검증.
	ai        *ai.Client                    // AI 학습 도우미 BFF. nil 허용 — 미설정 시 정적 hint_levels 폴백.
	events    events.Publisher              // lab-events 학습 이벤트 발행기. nil 허용 — 발행 생략(로컬/CI).
	userLocks *userLocks                    // 유저별 세션 생성 직렬화 — 단일 세션 제약의 동시요청 경합 완화.
}

// New는 설정/로거/세션 매니저/OIDC provider를 받아 Handler를 생성한다. sessions·authProvider·eventsPub는 nil 허용.
// 시작 시 임베드된 Lab DSL 콘텐츠를 로드하고, serial console 용 KubeVirt 클라이언트를 초기화한다.
// 클러스터 미연결(CI/로컬) 환경에서도 New가 성공하도록 둘 다 실패 시 nil/empty 폴백한다.
func New(cfg *config.Config, log *zap.Logger, sessions *kubevirt.Manager, validator validation.Publisher, eventsPub events.Publisher, authProvider *auth.Provider) *Handler {
	labs, err := content.Load()
	if err != nil {
		log.Error("lab content load failed; detail pages will lack steps", zap.Error(err))
		labs = map[string]content.LabContent{}
	}

	virt, err := kube.NewKubevirtClient()
	if err != nil {
		log.Warn("kubevirt client unavailable; live terminal disabled", zap.Error(err))
		virt = nil
	}

	aiClient := ai.New(cfg.AI.BaseURL, time.Duration(cfg.AI.TimeoutSeconds)*time.Second)
	if aiClient == nil {
		log.Warn("ai tutor not configured; hints fall back to static hint_levels")
	}

	return &Handler{
		cfg:       cfg,
		log:       log,
		auth:      authProvider,
		labs:      labs,
		sessions:  sessions,
		steps:     newStepStore(),
		virt:      virt,
		validator: validator,
		ai:        aiClient,
		events:    eventsPub,
		userLocks: newUserLocks(),
	}
}

// emitEvent는 학습 이벤트를 비동기로 발행한다(요청 경로를 막지 않음).
// publisher 미설정(nil)이면 no-op. 발행 실패는 경고 로그만 남긴다 — 분석용
// 데이터라 유실을 허용하고(at-most-once) 사용자 흐름에는 영향을 주지 않는다.
func (h *Handler) emitEvent(e events.Event) {
	if h.events == nil {
		return
	}
	e.Timestamp = time.Now().UTC()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.events.Publish(ctx, e); err != nil {
			h.log.Warn("학습 이벤트 발행 실패",
				zap.String("event_type", string(e.Type)),
				zap.String("session_id", e.SessionID),
				zap.Error(err),
			)
		}
	}()
}

// 프론트엔드 lib/api.ts의 ApiError 타입과 대응.
type errResp struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func (h *Handler) err(c *gin.Context, status int, msg string) {
	c.JSON(status, errResp{Error: msg})
}
