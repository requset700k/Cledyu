package handlers

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/content"
)

// labSummaryResponse는 Lab 목록 카드가 사용하는 공개 메타데이터다.
// 값의 원천은 mock summary가 아니라 임베드된 Lab YAML DSL이다.
type labSummaryResponse struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Difficulty  string   `json:"difficulty"`
	DurationMin int      `json:"duration_min"`
	Tags        []string `json:"tags"`
	VMType      string   `json:"vm_type"`
	StepCount   int      `json:"step_count"`
}

// labCatalogTieBreakOrder는 같은 난이도 안에서의 권장 학습 순서다.
// 예를 들어 Kubernetes는 이름이 "기초"여도 Docker 이후의 클러스터 개념을 다루므로 중급으로 분리한다.
var labCatalogTieBreakOrder = []string{
	"lab-linux-basics",
	"lab-docker-basics",
	"lab-k8s-basics",
	"lab-ansible-basics",
	"lab-terraform-basics",
	"lab-helm-advanced",
}

// labDifficultyRank는 카탈로그를 입문 → 중급 → 고급 순서로 보여주기 위한 정렬 기준이다.
// 난이도 값은 Lab DSL에 남기고, 같은 난이도 안에서의 세부 학습 순서는 labCatalogTieBreakOrder가 담당한다.
var labDifficultyRank = map[string]int{
	"beginner":     0,
	"intermediate": 1,
	"advanced":     2,
}

func publicLabSummary(lab content.LabContent) labSummaryResponse {
	return labSummaryResponse{
		ID:          lab.ID,
		Title:       lab.Title,
		Description: lab.Description,
		Difficulty:  lab.Difficulty,
		DurationMin: lab.DurationMin,
		Tags:        lab.Tags,
		VMType:      lab.VMType,
		StepCount:   len(lab.Steps),
	}
}

// orderedLabIDs는 map iteration 순서에 의존하지 않고 학습 흐름에 맞는 안정적인 카드 순서를 만든다.
// 1차 기준은 실제 실습 난이도이고, 같은 난이도 안에서는 기본기 → 컨테이너 → 클러스터/IaC → 패키징 순서로 묶는다.
// 새 Lab이 추가됐는데 난이도나 tie-break 순서가 빠져 있으면 목록 뒤로 보내 안전하게 노출한다.
func orderedLabIDs(labs map[string]content.LabContent) []string {
	ids := make([]string, 0, len(labs))
	for id := range labs {
		ids = append(ids, id)
	}

	tieBreak := make(map[string]int, len(labCatalogTieBreakOrder))
	for i, id := range labCatalogTieBreakOrder {
		tieBreak[id] = i
	}

	sort.SliceStable(ids, func(i, j int) bool {
		left := labs[ids[i]]
		right := labs[ids[j]]

		leftRank, ok := labDifficultyRank[left.Difficulty]
		if !ok {
			leftRank = len(labDifficultyRank)
		}
		rightRank, ok := labDifficultyRank[right.Difficulty]
		if !ok {
			rightRank = len(labDifficultyRank)
		}
		if leftRank != rightRank {
			return leftRank < rightRank
		}

		leftTie, ok := tieBreak[left.ID]
		if !ok {
			leftTie = len(tieBreak)
		}
		rightTie, ok := tieBreak[right.ID]
		if !ok {
			rightTie = len(tieBreak)
		}
		if leftTie != rightTie {
			return leftTie < rightTie
		}
		return left.ID < right.ID
	})

	return ids
}

// labStepResponse는 학습자 화면에 필요한 step 정보만 노출한다.
// checks는 validation-engine 전용 정답 조건이므로 API 응답에서 제외한다.
type labStepResponse struct {
	ID          int      `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Commands    []string `json:"commands,omitempty"`
	Hint        string   `json:"hint,omitempty"`
}

func publicSteps(steps []content.Step) []labStepResponse {
	out := make([]labStepResponse, 0, len(steps))
	for _, step := range steps {
		out = append(out, labStepResponse{
			ID:          step.ID,
			Title:       step.Title,
			Description: step.Description,
			Commands:    step.Commands,
			Hint:        step.Hint,
		})
	}
	return out
}

// ListLabs는 전체 lab 목록과 총 개수를 반환한다. 인증 필요.
// GET /api/v1/labs
func (h *Handler) ListLabs(c *gin.Context) {
	ids := orderedLabIDs(h.labs)
	items := make([]labSummaryResponse, 0, len(ids))
	for _, id := range ids {
		items = append(items, publicLabSummary(h.labs[id]))
	}

	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
	})
}

// GetLab은 id에 해당하는 lab을 반환한다. 없으면 404. 인증 필요.
// 목록과 상세 모두 같은 DSL 메타데이터를 원천으로 삼아 카드와 상세 화면의 단계 수가 어긋나지 않게 한다.
// GET /api/v1/labs/:id
func (h *Handler) GetLab(c *gin.Context) {
	id := c.Param("id")
	lab, ok := h.labs[id]
	if !ok {
		h.err(c, http.StatusNotFound, "lab not found")
		return
	}

	resp := gin.H{
		"id":           lab.ID,
		"title":        lab.Title,
		"description":  lab.Description,
		"difficulty":   lab.Difficulty,
		"duration_min": lab.DurationMin,
		"tags":         lab.Tags,
		"vm_type":      lab.VMType,
		"step_count":   len(lab.Steps),
		"steps":        publicSteps(lab.Steps),
		"environment":  lab.Environment,
	}
	c.JSON(http.StatusOK, resp)
}
