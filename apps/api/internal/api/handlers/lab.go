package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// mockLabs는 Lab DSL 명세 확정 전까지 사용하는 하드코딩 데이터.
var mockLabs = []gin.H{
	{
		"id":           "lab-k8s-basics",
		"title":        "Kubernetes 기초",
		"description":  "Pod, Deployment, Service 기본 개념 실습",
		"difficulty":   "beginner",
		"duration_min": 60,
		"tags":         []string{"kubernetes", "pod", "deployment"},
		"vm_type":      "lab-small",
		"step_count":   3,
		"steps": []gin.H{
			{"id": 1, "title": "Pod 생성", "description": "nginx Pod를 직접 생성한다"},
			{"id": 2, "title": "Deployment 생성", "description": "replica 3개짜리 Deployment를 만든다"},
			{"id": 3, "title": "Service 노출", "description": "ClusterIP Service로 Pod를 연결한다"},
		},
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
		"steps": []gin.H{
			{"id": 1, "title": "이미지 빌드", "description": "Dockerfile로 이미지를 빌드한다"},
			{"id": 2, "title": "컨테이너 실행", "description": "빌드한 이미지로 컨테이너를 실행한다"},
		},
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
		"steps": []gin.H{
			{"id": 1, "title": "인벤토리 작성", "description": "호스트 그룹과 변수를 인벤토리 파일로 정의한다"},
			{"id": 2, "title": "플레이북 실행", "description": "nginx 설치 플레이북을 작성하고 실행한다"},
			{"id": 3, "title": "멱등성 확인", "description": "동일 플레이북을 재실행해 변경 없음을 검증한다"},
		},
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
		"steps": []gin.H{
			{"id": 1, "title": "Provider 설정", "description": "로컬 Provider를 구성하고 init을 실행한다"},
			{"id": 2, "title": "리소스 선언", "description": "HCL로 리소스를 선언하고 plan 결과를 확인한다"},
			{"id": 3, "title": "State 관리", "description": "apply 후 state 파일 구조를 분석한다"},
		},
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
		"steps": []gin.H{
			{"id": 1, "title": "Chart 구조 이해", "description": "Chart.yaml, values.yaml, templates 구조를 파악한다"},
			{"id": 2, "title": "템플릿 작성", "description": "deployment.yaml 템플릿을 직접 작성한다"},
			{"id": 3, "title": "배포 및 검증", "description": "helm install로 배포 후 rollout status를 확인한다"},
		},
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
// GET /api/v1/labs/:id
func (h *Handler) GetLab(c *gin.Context) {
	id := c.Param("id")
	for _, lab := range mockLabs {
		if lab["id"] == id {
			c.JSON(http.StatusOK, lab)
			return
		}
	}
	h.err(c, http.StatusNotFound, "lab not found")
}
