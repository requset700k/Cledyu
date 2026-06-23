package session

import (
	"context"
	"testing"
	"time"
)

// fakeProvider는 Provider 의 인메모리 가짜다 — 디스패처 라우팅 검증용.
type fakeProvider struct {
	name     string
	sessions map[string]*Session
	created  []string
}

func newFake(name string) *fakeProvider {
	return &fakeProvider{name: name, sessions: map[string]*Session{}}
}

func (f *fakeProvider) Create(_ context.Context, id, lab, user string, _ BootInit) (*Session, error) {
	s := &Session{ID: id, LabID: lab, UserID: user, Status: "provisioning", Provider: f.name}
	f.sessions[id] = s
	f.created = append(f.created, id)
	return s, nil
}
func (f *fakeProvider) Get(_ context.Context, id string) (*Session, error) {
	if s, ok := f.sessions[id]; ok {
		return s, nil
	}
	return nil, ErrNotFound
}
func (f *fakeProvider) Delete(_ context.Context, id string) error {
	if _, ok := f.sessions[id]; !ok {
		return ErrNotFound
	}
	delete(f.sessions, id)
	return nil
}
func (f *fakeProvider) FindActiveByUser(_ context.Context, user string) (string, error) {
	for id, s := range f.sessions {
		if s.UserID == user {
			return id, nil
		}
	}
	return "", nil
}
func (f *fakeProvider) CountActiveSessions(_ context.Context) (int, error) {
	return len(f.sessions), nil
}
func (f *fakeProvider) ReapStuckSessions(_ context.Context, _ time.Duration) ([]string, error) {
	return nil, nil
}
func (f *fakeProvider) ReapExpiredSessions(_ context.Context) ([]string, error) { return nil, nil }
func (f *fakeProvider) VMIAddress(_ context.Context, id string) (string, error) {
	if _, ok := f.sessions[id]; ok {
		return f.name + "-addr", nil
	}
	return "", ErrNotFound
}

func TestDispatcher_RoutesToOnpremUntilFull(t *testing.T) {
	onprem, overflow := newFake("kubevirt"), newFake("ec2")
	d := NewDispatcher(onprem, overflow, 2, nil)
	ctx := context.Background()

	// 처음 2개는 온프렘(cap=2).
	s1, _ := d.Create(ctx, "s1", "lab-a", "u1", BootInit{})
	s2, _ := d.Create(ctx, "s2", "lab-a", "u2", BootInit{})
	if s1.Provider != "kubevirt" || s2.Provider != "kubevirt" {
		t.Fatalf("first two should be kubevirt: %s,%s", s1.Provider, s2.Provider)
	}
	// 온프렘이 cap 에 도달했으니 3번째는 EC2 오버플로우.
	s3, _ := d.Create(ctx, "s3", "lab-a", "u3", BootInit{})
	if s3.Provider != "ec2" {
		t.Errorf("third should overflow to ec2, got %s", s3.Provider)
	}
}

func TestDispatcher_NoOverflowStaysOnprem(t *testing.T) {
	onprem := newFake("kubevirt")
	d := NewDispatcher(onprem, nil, 1, nil)
	ctx := context.Background()
	// overflow 가 nil 이면 cap 초과해도 온프렘에 생성한다(EC2 비활성).
	_, _ = d.Create(ctx, "s1", "lab-a", "u1", BootInit{})
	s2, _ := d.Create(ctx, "s2", "lab-a", "u2", BootInit{})
	if s2.Provider != "kubevirt" {
		t.Errorf("no overflow → kubevirt, got %s", s2.Provider)
	}
}

func TestDispatcher_GetDeleteAcrossProviders(t *testing.T) {
	onprem, overflow := newFake("kubevirt"), newFake("ec2")
	d := NewDispatcher(onprem, overflow, 0, nil) // cap 0 → 항상 오버플로우
	ctx := context.Background()

	_, _ = d.Create(ctx, "e1", "lab-a", "u1", BootInit{})
	if len(overflow.created) != 1 {
		t.Fatal("expected create on overflow")
	}
	// Get 은 온프렘에 없으면 오버플로우에서 찾는다.
	got, err := d.Get(ctx, "e1")
	if err != nil || got.Provider != "ec2" {
		t.Errorf("Get cross-provider: %+v, %v", got, err)
	}
	// VMIAddress 도 오버플로우로 폴스루.
	if addr, err := d.VMIAddress(ctx, "e1"); err != nil || addr != "ec2-addr" {
		t.Errorf("VMIAddress = %q, %v", addr, err)
	}
	// Delete 도 오버플로우에서.
	if err := d.Delete(ctx, "e1"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if n, _ := d.CountActiveSessions(ctx); n != 0 {
		t.Errorf("count after delete = %d", n)
	}
}

func TestDispatcher_CountIsSum(t *testing.T) {
	onprem, overflow := newFake("kubevirt"), newFake("ec2")
	d := NewDispatcher(onprem, overflow, 1, nil)
	ctx := context.Background()
	_, _ = d.Create(ctx, "s1", "lab-a", "u1", BootInit{}) // onprem
	_, _ = d.Create(ctx, "s2", "lab-a", "u2", BootInit{}) // overflow (cap=1)
	n, _ := d.CountActiveSessions(ctx)
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	// 단일 세션 제약: 오버플로우 유저도 FindActiveByUser 로 찾혀야 한다.
	if id, _ := d.FindActiveByUser(ctx, "u2"); id != "s2" {
		t.Errorf("FindActiveByUser(u2) = %q, want s2", id)
	}
}
