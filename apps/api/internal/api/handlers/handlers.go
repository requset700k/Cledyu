// Package handlers는 도메인별 HTTP 핸들러를 구현한다 (health, lab, user, auth).
package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/kube"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"go.uber.org/zap"
	"kubevirt.io/client-go/kubecli"
)

// Handler는 모든 HTTP 핸들러의 공유 의존성을 보관한다.
type Handler struct {
	cfg      *config.Config
	log      *zap.Logger
	labs     map[string]content.LabContent // lab id → DSL 콘텐츠(스텝). GetLab/세션에서 사용.
	sessions *kubevirt.Manager             // KubeVirt VM 수명주기. nil 허용 — 클러스터 미연결 시 세션 API 503.
	steps    *stepStore                    // STUB(검증엔진 연동 전): 세션별 스텝 진행 상태 in-memory.
	virt     kubecli.KubevirtClient        // VM serial console 접속용 KubeVirt 클라이언트. nil이면 콘솔 비활성.
}

// New는 설정/로거/세션 매니저를 받아 Handler를 생성한다. sessions는 nil 허용.
// 시작 시 임베드된 Lab DSL 콘텐츠를 로드하고, serial console 용 KubeVirt 클라이언트를 초기화한다.
// 클러스터 미연결(CI/로컬) 환경에서도 New가 성공하도록 둘 다 실패 시 nil/empty 폴백한다.
func New(cfg *config.Config, log *zap.Logger, sessions *kubevirt.Manager) *Handler {
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

	return &Handler{
		cfg:      cfg,
		log:      log,
		labs:     labs,
		sessions: sessions,
		steps:    newStepStore(),
		virt:     virt,
	}
}

// 프론트엔드 lib/api.ts의 ApiError 타입과 대응.
type errResp struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func (h *Handler) err(c *gin.Context, status int, msg string) {
	c.JSON(status, errResp{Error: msg})
}
