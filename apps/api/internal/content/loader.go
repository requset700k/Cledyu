// Package content는 Lab 콘텐츠(스텝·검증 체크)를 임베드된 YAML DSL로부터 로드한다.
//
// 임시 DSL: 정식 Lab DSL 스키마 확정 전까지 사용한다(소유: Lab DSL 도메인, 추후 협의).
// 콘텐츠를 레포 루트 labs/ 가 아닌 이 패키지 하위(labs/)에 두는 이유는,
// apps/api 가 자체 Go 모듈이라 //go:embed 가 모듈 경계 밖(레포 루트)을 임베드할 수 없기 때문이다.
package content

import (
	"embed"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed labs/*.yaml
var labsFS embed.FS

// Check는 단계 완료를 판정하는 검증 항목이다.
// 필드 구성은 검증엔진(apps/validation-engine/internal/model)의 Check 와 정렬되어 있어,
// 실제 검증엔진으로 그대로 전달(publish)할 수 있다.
type Check struct {
	Type string `yaml:"type" json:"type"`
	// Command 의 JSON 태그는 검증엔진 model.Check(json:"cmd")와 일치시켜야 한다.
	// YAML 태그는 랩 DSL 키(랩 저자는 계속 `command:` 작성)를 유지한다.
	// 불일치 시 Kafka 발행→소비 과정에서 cmd 가 유실돼 command 타입 체크가 전부 깨진다.
	Command    string `yaml:"command,omitempty" json:"cmd,omitempty"`
	Path       string `yaml:"path,omitempty" json:"path,omitempty"`
	URL        string `yaml:"url,omitempty" json:"url,omitempty"`
	Name       string `yaml:"name,omitempty" json:"name,omitempty"`
	Expect     string `yaml:"expect,omitempty" json:"expect,omitempty"`
	ExpectCode int    `yaml:"expect_code,omitempty" json:"expect_code,omitempty"`
	// Timeout 은 이 체크 하나의 실행 제한 시간(초). 0이면 검증엔진 기본값(20s).
	// JSON 태그는 검증엔진 model.Check(json:"timeout")와 일치시킨다.
	Timeout int `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// Step은 랩의 단일 실습 단계다.
type Step struct {
	ID          int      `yaml:"id" json:"id"`
	Title       string   `yaml:"title" json:"title"`
	Description string   `yaml:"description" json:"description"`
	Commands    []string `yaml:"commands,omitempty" json:"commands,omitempty"`
	Hint        string   `yaml:"hint,omitempty" json:"hint,omitempty"`
	// HintLevels는 단계별 구체성의 정적 힌트 3개(레벨 1 개념 → 2 방향 → 3 구체)다.
	// AI 도우미(ai-tutor) 미가용 시의 최종 폴백이자(기획서 3.5 다층 fallback),
	// 레거시 단일 hint 의 후속이다. 비어 있으면 Hint 가 전 레벨 공용으로 쓰인다.
	HintLevels []string `yaml:"hint_levels,omitempty" json:"hint_levels,omitempty"`
	Checks     []Check  `yaml:"checks,omitempty" json:"checks,omitempty"`
}

// StaticHint는 요청 레벨(1~3)에 해당하는 정적 힌트를 반환한다.
// 해당 레벨이 없으면 마지막 hint_levels 항목 → 레거시 hint 순으로 폴백한다.
func (s Step) StaticHint(level int) string {
	if len(s.HintLevels) > 0 {
		idx := level - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(s.HintLevels) {
			idx = len(s.HintLevels) - 1
		}
		return s.HintLevels[idx]
	}
	return s.Hint
}

// InitSpec은 세션 VM 부팅 시 cloud-init 으로 실행되는 랩별 초기화다(기획서 Lab DSL 의 initial_state).
// packages 는 cloud-init `packages:` 로(apt), runcmd 는 `runcmd:` 끝에 그대로 추가된다.
// 랩 저자(Git PR)만 작성할 수 있는 신뢰 입력이며 root 로 실행된다 — 학생 VM 은 어차피
// 학생이 root 인 격리 VM 이라 권한 상승 표면이 아니다.
type InitSpec struct {
	Packages []string `yaml:"packages,omitempty" json:"-"`
	Runcmd   []string `yaml:"runcmd,omitempty" json:"-"`
}

// IsZero는 초기화 작업이 없는지 반환한다.
func (i InitSpec) IsZero() bool { return len(i.Packages) == 0 && len(i.Runcmd) == 0 }

// LabContent는 한 랩의 DSL 콘텐츠(메타데이터 + 스텝)다.
type LabContent struct {
	ID          string   `yaml:"id" json:"id"`
	Title       string   `yaml:"title" json:"title"`
	Description string   `yaml:"description" json:"description"`
	Difficulty  string   `yaml:"difficulty" json:"difficulty"`
	DurationMin int      `yaml:"duration_min" json:"duration_min"`
	VMType      string   `yaml:"vm_type" json:"vm_type"`
	Tags        []string `yaml:"tags" json:"tags"`
	// Environment는 세션 VM 실행 환경(예: "ubuntu", "k3s").
	// Phase-1에서는 "ubuntu" 랩만 실시간 터미널을 제공한다(k3s는 미구현).
	Environment string `yaml:"environment,omitempty" json:"environment,omitempty"`
	// IDE: true 면 세션 VM 에 code-server(브라우저 VS Code)를 띄우고 IDE 탭을 제공한다.
	// init 에서 code-server 설치/기동을 함께 선언해야 한다(예: lab-terraform-basics).
	IDE   bool     `yaml:"ide,omitempty" json:"ide,omitempty"`
	Init  InitSpec `yaml:"init,omitempty" json:"-"` // 프론트에 노출할 필요 없는 서버 전용 필드
	Steps []Step   `yaml:"steps" json:"steps"`
}

// HasLiveTerminal은 이 랩이 실시간 KubeVirt 터미널을 제공하는지 반환한다.
// Phase-1: 순수 Ubuntu 환경만 실제 셸 연결을 지원한다.
func (lc LabContent) HasLiveTerminal() bool {
	return lc.Environment == "ubuntu"
}

// Load는 임베드된 labs/*.yaml 을 모두 파싱해 lab id → LabContent 맵으로 반환한다.
// 파싱 실패나 중복/누락 id는 에러로 반환해 잘못된 콘텐츠의 조기 검출을 돕는다.
func Load() (map[string]LabContent, error) {
	entries, err := labsFS.ReadDir("labs")
	if err != nil {
		return nil, fmt.Errorf("read labs dir: %w", err)
	}

	out := make(map[string]LabContent, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := labsFS.ReadFile("labs/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}

		var lc LabContent
		if err := yaml.Unmarshal(data, &lc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if lc.ID == "" {
			return nil, fmt.Errorf("%s: lab id is empty", e.Name())
		}
		if _, dup := out[lc.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate lab id %q", e.Name(), lc.ID)
		}

		// 스텝을 id 오름차순으로 정렬해 응답 순서를 안정화한다.
		sort.Slice(lc.Steps, func(i, j int) bool { return lc.Steps[i].ID < lc.Steps[j].ID })
		out[lc.ID] = lc
	}
	return out, nil
}
