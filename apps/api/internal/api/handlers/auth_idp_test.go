package handlers

import "testing"

func TestNormalizeIdP(t *testing.T) {
	cases := map[string]string{
		"google": "google",
		"kakao":  "kakao",
		"naver":  "naver",
		"":       "",
		"evil":   "",
		"GOOGLE": "", // 대소문자 정확 일치만 허용
		"google ": "",
	}
	for in, want := range cases {
		if got := normalizeIdP(in); got != want {
			t.Errorf("normalizeIdP(%q) = %q, want %q", in, got, want)
		}
	}
}
