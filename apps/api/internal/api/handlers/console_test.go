package handlers

import "testing"

// parseResizeFrame는 브라우저가 보낸 TextMessage 가 리사이즈 제어 프레임일 때만 cols/rows 를 돌려줘야 한다.
// 키 입력(binary)·서버 에러 텍스트·형식 불일치는 모두 일반 데이터로 흘려보내도록 ok=false 여야 한다.
func TestParseResizeFrame(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantCols int
		wantRows int
		wantOK   bool
	}{
		{"valid resize", `{"type":"resize","cols":120,"rows":40}`, 120, 40, true},
		{"valid resize spaced", `{"type": "resize", "cols": 80, "rows": 24}`, 80, 24, true},
		{"server error text", "\r\n[VM 콘솔 연결 실패: timeout]\r\n", 0, 0, false},
		{"plain keystroke", "ls -la\n", 0, 0, false},
		{"wrong type", `{"type":"data","cols":80,"rows":24}`, 0, 0, false},
		{"zero cols", `{"type":"resize","cols":0,"rows":24}`, 0, 0, false},
		{"negative rows", `{"type":"resize","cols":80,"rows":-5}`, 0, 0, false},
		{"absurd cols rejected", `{"type":"resize","cols":99999,"rows":24}`, 0, 0, false},
		{"empty", "", 0, 0, false},
		{"not json", "{not json", 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cols, rows, ok := parseResizeFrame([]byte(tc.in))
			if ok != tc.wantOK || cols != tc.wantCols || rows != tc.wantRows {
				t.Errorf("parseResizeFrame(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tc.in, cols, rows, ok, tc.wantCols, tc.wantRows, tc.wantOK)
			}
		})
	}
}
