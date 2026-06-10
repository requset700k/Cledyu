package content

import "testing"

// 임베드된 모든 랩이 파싱되고, 각 스텝에 3단계 hint_levels 가 채워져 있는지 검증한다.
// hint_levels 는 AI 도우미 미가용 시의 최종 폴백이라 콘텐츠 누락을 조기 검출해야 한다.
func TestLoad_HintLevels(t *testing.T) {
	labs, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(labs) == 0 {
		t.Fatal("no labs loaded")
	}
	for id, lc := range labs {
		for _, st := range lc.Steps {
			if len(st.HintLevels) != 3 {
				t.Errorf("%s step %d: expected 3 hint_levels, got %d", id, st.ID, len(st.HintLevels))
			}
		}
	}
}

func TestStaticHint(t *testing.T) {
	s := Step{Hint: "legacy", HintLevels: []string{"l1", "l2", "l3"}}
	cases := []struct {
		level int
		want  string
	}{
		{1, "l1"}, {2, "l2"}, {3, "l3"},
		{0, "l1"},  // 하한 클램프
		{99, "l3"}, // 상한 클램프
	}
	for _, c := range cases {
		if got := s.StaticHint(c.level); got != c.want {
			t.Errorf("StaticHint(%d) = %q, want %q", c.level, got, c.want)
		}
	}
	// hint_levels 가 없으면 레거시 hint 로 폴백.
	legacy := Step{Hint: "legacy"}
	if got := legacy.StaticHint(2); got != "legacy" {
		t.Errorf("expected legacy hint fallback, got %q", got)
	}
}
