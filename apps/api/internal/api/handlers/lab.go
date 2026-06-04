package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// mockLabs는 Lab DSL 명세 확정 전까지 사용하는 하드코딩 데이터.
// steps 상세는 실습 상세 페이지(PR 11) 구현 시 추가.
var mockLabs = []gin.H{
	{
		"id":           "lab-linux-basics",
		"title":        "Linux 기초",
		"description":  "파일·디렉터리, 권한, 프로세스 등 리눅스 셸 기본기 실습",
		"difficulty":   "beginner",
		"duration_min": 45,
		"tags":         []string{"linux", "shell", "ubuntu"},
		"vm_type":      "lab-small",
		"step_count":   3,
	},
	{
		"id":           "lab-k8s-basics",
		"title":        "Kubernetes 기초",
		"description":  "Pod, Deployment, Service 기본 개념 실습",
		"difficulty":   "beginner",
		"duration_min": 60,
		"tags":         []string{"kubernetes", "pod", "deployment"},
		"vm_type":      "lab-small",
		"step_count":   3,
	},
	{
		"id":           "lab-docker-basics",
		"title":        "Docker 기초",
		"description":  "이미지 빌드, 컨테이너 실행, 레이어 이해",
		"difficulty":   "beginner",
		"duration_min": 45,
		"tags":         []string{"docker", "container", "image"},
		"vm_type":      "lab-small",
		"step_count":   2,
	},
	{
		"id":           "lab-ansible-basics",
		"title":        "Ansible 기초",
		"description":  "인벤토리, 플레이북 작성, 멱등성 이해",
		"difficulty":   "intermediate",
		"duration_min": 60,
		"tags":         []string{"ansible", "automation", "playbook"},
		"vm_type":      "lab-small",
		"step_count":   3,
	},
	{
		"id":           "lab-terraform-basics",
		"title":        "Terraform 기초",
		"description":  "HCL 문법, 리소스 선언, state 관리",
		"difficulty":   "intermediate",
		"duration_min": 75,
		"tags":         []string{"terraform", "iac", "infra"},
		"vm_type":      "lab-small",
		"step_count":   3,
	},
	{
		"id":           "lab-helm-advanced",
		"title":        "Helm 고급",
		"description":  "Helm chart 작성, 패키징, 배포 자동화",
		"difficulty":   "advanced",
		"duration_min": 90,
		"tags":         []string{"helm", "kubernetes", "gitops"},
		"vm_type":      "lab-medium",
		"step_count":   3,
	},
}

// ListLabs는 전체 lab 목록과 총 개수를 반환한다. 인증 필요.
// GET /api/v1/labs
func (h *Handler) ListLabs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"items": mockLabs,
		"total": len(mockLabs),
	})
}

// GetLab은 id에 해당하는 lab을 반환한다. 없으면 404. 인증 필요.
// 해당 lab의 DSL 콘텐츠가 있으면 응답에 steps(스텝 상세)를 머지한다.
// GET /api/v1/labs/:id
func (h *Handler) GetLab(c *gin.Context) {
	id := c.Param("id")
	for _, lab := range mockLabs {
		if lab["id"] != id {
			continue
		}
		// mockLabs를 변경하지 않도록 복사 후 steps/environment를 덧붙인다.
		resp := make(gin.H, len(lab)+2)
		for k, v := range lab {
			resp[k] = v
		}
		if lc, ok := h.labs[id]; ok {
			resp["steps"] = lc.Steps
			resp["environment"] = lc.Environment
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	h.err(c, http.StatusNotFound, "lab not found")
}
