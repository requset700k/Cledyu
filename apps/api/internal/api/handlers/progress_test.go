package handlers

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/requset700k/cledyu/api/internal/store"
	"github.com/requset700k/cledyu/api/internal/validation"
	"go.uber.org/zap"
)

// fakePersistence는 persistence 의 in-memory 테스트 더블이다.
type fakePersistence struct {
	mu          sync.Mutex
	progress    map[string]store.SessionProgress
	users       map[string]string // id → role
	completions map[string]string // user|lab → session
	saves       int
}

func newFakePersistence() *fakePersistence {
	return &fakePersistence{
		progress:    map[string]store.SessionProgress{},
		users:       map[string]string{},
		completions: map[string]string{},
	}
}

func (f *fakePersistence) SaveProgress(_ context.Context, sessionID string, p store.SessionProgress) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress[sessionID] = p
	f.saves++
	return nil
}

func (f *fakePersistence) LoadProgress(_ context.Context, sessionID string) (*store.SessionProgress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.progress[sessionID]
	if !ok {
		return nil, nil
	}
	return &p, nil
}

func (f *fakePersistence) DeleteProgress(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.progress, sessionID)
	return nil
}

func (f *fakePersistence) UpsertUser(_ context.Context, id, _, _, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[id] = role
	return nil
}

func (f *fakePersistence) ListUsers(_ context.Context, limit int) ([]store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.User, 0, len(f.users))
	for id, role := range f.users {
		out = append(out, store.User{ID: id, Role: role})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakePersistence) ListCompletionsByUser(_ context.Context, userID string) ([]store.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Completion, 0)
	for key, sess := range f.completions {
		if strings.HasPrefix(key, userID+"|") {
			out = append(out, store.Completion{LabID: strings.TrimPrefix(key, userID+"|"), SessionID: sess})
		}
	}
	return out, nil
}

func (f *fakePersistence) SetUserRole(_ context.Context, userID, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[userID]; ok {
		f.users[userID] = role
	}
	return nil
}

func (f *fakePersistence) RecordCompletion(_ context.Context, userID, labID, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := userID + "|" + labID
	if _, exists := f.completions[key]; !exists {
		f.completions[key] = sessionID
	}
	return nil
}

func twoStepSeed() *sessionSteps {
	return &sessionSteps{
		LabID:  "lab-linux-basics",
		UserID: "alice",
		Steps: []stepState{
			{StepID: 1, Status: "active"},
			{StepID: 2, Status: "pending"},
		},
		CurrentStep: 1,
	}
}

// put → 변경(withSession dirty) → DB 스냅샷이 최신 상태를 반영하는지 검증한다(write-through).
func TestProgress_WriteThrough(t *testing.T) {
	db := newFakePersistence()
	st := newStepStore(db, zap.NewNop())

	st.put("s1", twoStepSeed())
	if got := db.progress["s1"]; got.LabID != "lab-linux-basics" || len(got.Steps) != 2 {
		t.Fatalf("put must persist snapshot, got %+v", got)
	}

	st.withSession("s1", func(ss *sessionSteps) bool {
		ss.Steps[0].Status = "passed"
		ss.CurrentStep = 2
		return true
	})
	if got := db.progress["s1"]; got.Steps[0].Status != "passed" || got.CurrentStep != 2 {
		t.Fatalf("dirty mutation must persist, got %+v", got)
	}

	// dirty=false 면 추가 저장이 없어야 한다(읽기 경로의 불필요한 DB 쓰기 방지).
	before := db.saves
	st.withSession("s1", func(*sessionSteps) bool { return false })
	if db.saves != before {
		t.Fatalf("read-only access must not persist, saves %d → %d", before, db.saves)
	}
}

// 캐시에 없는 세션을 DB에서 적재하는지 검증한다(load-on-miss — API 재시작 복원 경로).
func TestProgress_LoadOnMiss(t *testing.T) {
	db := newFakePersistence()
	db.progress["s1"] = *toStoreProgress(twoStepSeed())

	st := newStepStore(db, zap.NewNop()) // 캐시는 비어 있음(재시작 직후 상황)
	var owner string
	found := st.withSession("s1", func(ss *sessionSteps) bool {
		owner = ss.UserID
		return false
	})
	if !found || owner != "alice" {
		t.Fatalf("expected load-on-miss restore, found=%v owner=%q", found, owner)
	}
}

// take 는 캐시·DB 양쪽에서 제거하고, 캐시 미스여도 DB 사본을 돌려준다.
func TestProgress_TakeDeletesBoth(t *testing.T) {
	db := newFakePersistence()
	db.progress["s1"] = *toStoreProgress(twoStepSeed())
	st := newStepStore(db, zap.NewNop())

	ss, ok := st.take("s1")
	if !ok || ss.UserID != "alice" {
		t.Fatalf("take must return DB copy on cache miss, ok=%v ss=%+v", ok, ss)
	}
	if _, exists := db.progress["s1"]; exists {
		t.Fatal("take must delete the DB row")
	}
	if found := st.withSession("s1", func(*sessionSteps) bool { return false }); found {
		t.Fatal("session must be gone after take")
	}
}

// 전 스텝 통과 시 lab_completions 가 기록되는지 검증한다(ApplyValidationResult 경로).
func TestProgress_RecordsCompletion(t *testing.T) {
	db := newFakePersistence()
	h := &Handler{log: zap.NewNop(), steps: newStepStore(db, zap.NewNop()), db: db}
	seed := twoStepSeed()
	seed.Steps[0].Status = "passed" // 1번은 이미 통과
	h.steps.put("s1", seed)

	h.ApplyValidationResult(validation.ValidationResult{SessionID: "s1", StepID: 2, Passed: true})

	if got := db.completions["alice|lab-linux-basics"]; got != "s1" {
		t.Fatalf("expected completion recorded, got %q", got)
	}
}

// db 가 nil 이면(로컬/CI) 전 경로가 in-memory 로만 동작한다(회귀 가드).
func TestProgress_NilDBInMemoryOnly(t *testing.T) {
	st := newStepStore(nil, nil)
	st.put("s1", twoStepSeed())
	if found := st.withSession("s1", func(*sessionSteps) bool { return true }); !found {
		t.Fatal("in-memory put/get must work without db")
	}
	if _, ok := st.take("s1"); !ok {
		t.Fatal("in-memory take must work without db")
	}
}
