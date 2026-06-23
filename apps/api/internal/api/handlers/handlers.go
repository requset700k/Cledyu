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
	"github.com/requset700k/cledyu/api/internal/lock"
	"github.com/requset700k/cledyu/api/internal/session"
	"github.com/requset700k/cledyu/api/internal/store"
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
	sessions  session.Provider              // 세션 VM 수명주기(KubeVirt 또는 EC2 오버플로우 디스패처). nil 허용 — 미연결 시 세션 API 503.
	steps     *stepStore                    // 세션별 스텝 진행 상태 — in-memory 캐시 + DB write-through(progress.go).
	virt      kubecli.KubevirtClient        // VM serial console 접속용 KubeVirt 클라이언트. nil이면 콘솔 비활성.
	validator validation.Publisher          // validation-requests Kafka 발행기. nil이면 debug 모드에서 mock 검증.
	ai        *ai.Client                    // AI 학습 도우미 BFF. nil 허용 — 미설정 시 정적 hint_levels 폴백.
	events    events.Publisher              // lab-events 학습 이벤트 발행기. nil 허용 — 발행 생략(로컬/CI).
	db        persistence                   // PostgreSQL 영속 계층. nil 허용 — in-memory 전용(로컬/CI).
	kcAdmin   roleAssigner                  // Keycloak Admin(역할 승격). nil 허용 — 미설정 시 역할 승격 API 501.
	locks     lock.Locker                   // 유저별 세션 생성 직렬화 — Redis 분산 락 또는 in-memory(MemLocker).
}

// roleAssigner는 Keycloak 역할 승격 의존성이다(*auth.AdminClient 가 구현).
type roleAssigner interface {
	AssignRealmRole(ctx context.Context, userID, role string) error
}

// New는 설정/로거/세션 매니저/OIDC provider를 받아 Handler를 생성한다.
// sessions·authProvider·eventsPub·db 는 nil 허용. 시작 시 임베드된 Lab DSL 콘텐츠를
// 로드하고, serial console 용 KubeVirt 클라이언트를 초기화한다.
// 클러스터 미연결(CI/로컬) 환경에서도 New가 성공하도록 둘 다 실패 시 nil/empty 폴백한다.
func New(cfg *config.Config, log *zap.Logger, sessions session.Provider, validator validation.Publisher, eventsPub events.Publisher, db *store.Store, locks lock.Locker, authProvider *auth.Provider) *Handler {
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

	// *store.Store(nil) 을 인터페이스에 그대로 담으면 non-nil 인터페이스가 되는
	// Go 함정을 피한다 — nil 포인터면 인터페이스도 nil 로 둔다.
	var p persistence
	if db != nil {
		p = db
	}

	// 역할 승격 service-account — 미설정 시 nil(역할 승격 API 만 비활성).
	var kc roleAssigner
	if ac := auth.NewAdminClient(cfg.Keycloak); ac != nil {
		kc = ac
	} else {
		log.Warn("keycloak admin client not configured; role promotion API disabled")
	}

	// 락 미주입(로컬/CI 등 main 외 경로)이면 in-memory 폴백으로 안전하게 동작한다.
	if locks == nil {
		locks = lock.NewMemLocker()
	}

	return &Handler{
		cfg:       cfg,
		log:       log,
		auth:      authProvider,
		labs:      labs,
		sessions:  sessions,
		steps:     newStepStore(p, log),
		virt:      virt,
		validator: validator,
		ai:        aiClient,
		events:    eventsPub,
		db:        p,
		kcAdmin:   kc,
		locks:     locks,
	}
}

// upsertUser는 로그인/리프레시 시점의 Keycloak 신원을 앱 DB로 미러링한다(비동기).
// DB 미설정이면 no-op. 실패는 경고 로그만 — 로그인 흐름을 막지 않는다.
func (h *Handler) upsertUser(id *auth.Identity) {
	if h.db == nil || id == nil || id.Subject == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()
		if err := h.db.UpsertUser(ctx, id.Subject, id.Email, id.Name, id.Role()); err != nil {
			h.log.Warn("유저 미러링 실패", zap.String("user_id", id.Subject), zap.Error(err))
		}
	}()
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
