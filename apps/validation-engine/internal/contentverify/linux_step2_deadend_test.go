package contentverify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/requset700k/cledyu/validation-engine/internal/checker"
	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

// vmFS는 경로별 파일 존재를 흉내내는 테스트용 executor다.
// checker가 file_exists에 "test -f <path>", file_absent에 "test ! -f <path>"를 보내는 것에 맞춘다.
type vmFS struct{ present map[string]bool }

func (v vmFS) Exec(_ context.Context, cmd string) (string, error) {
	if strings.HasPrefix(cmd, "test ! -f ") {
		p := strings.TrimPrefix(cmd, "test ! -f ")
		if v.present[p] {
			return "", errors.New("exit status 1") // 파일 있음 → 부재 검사 실패
		}
		return "", nil // 부재 → 통과
	}
	if strings.HasPrefix(cmd, "test -f ") {
		p := strings.TrimPrefix(cmd, "test -f ")
		if v.present[p] {
			return "", nil
		}
		return "", errors.New("exit status 1") // 파일 없음 → 존재 검사 실패
	}
	return "", nil
}

func (v vmFS) DefaultTimeout() time.Duration { return 20 * time.Second }
func (v vmFS) Close()                        {}

func linuxStep2Checks(t *testing.T) []model.Check {
	t.Helper()
	var out []model.Check
	for _, lab := range loadLabs(t) {
		if lab.ID != "lab-linux-basics" {
			continue
		}
		for _, s := range lab.Steps {
			if s.ID != 2 {
				continue
			}
			for _, wc := range s.Checks {
				out = append(out, toModelCheck(t, wc))
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("lab-linux-basics step2 체크를 찾지 못함")
	}
	return out
}

// step2는 .log를 backup에 '복사'하는 단계다. 복귀불가(폭포수) 모델에서, mv로 원본을 logs에서
// 옮겨버리면 backup엔 사본이 있어 통과하지만 logs 원본이 사라져 step5(tar logs)·step6(app1.log
// 심링크)에서 막히고 되돌릴 수 없다(dead-end). 따라서 step2는 원본 보존도 검증해야 한다.
func TestLinuxStep2_RequiresOriginalsRemain(t *testing.T) {
	step2 := linuxStep2Checks(t)

	// cp: backup 사본 + logs 원본 모두 존재 (debug.txt는 backup에 없음 → file_absent 통과)
	cp := vmFS{present: map[string]bool{
		"/home/lab/work/backup/app1.log": true,
		"/home/lab/work/backup/app2.log": true,
		"/home/lab/work/logs/app1.log":   true,
		"/home/lab/work/logs/app2.log":   true,
	}}
	if _, ok := checker.RunAll(context.Background(), cp, step2); !ok {
		t.Error("cp(원본 보존) 정답은 step2를 통과해야 함")
	}

	// mv: backup 사본은 있지만 logs 원본이 사라짐 → step2가 잡아야 한다.
	mv := vmFS{present: map[string]bool{
		"/home/lab/work/backup/app1.log": true,
		"/home/lab/work/backup/app2.log": true,
	}}
	if _, ok := checker.RunAll(context.Background(), mv, step2); ok {
		t.Error("mv(원본 유실)는 step2에서 탈락해야 함 — 안 그러면 step5/6 dead-end")
	}
}
