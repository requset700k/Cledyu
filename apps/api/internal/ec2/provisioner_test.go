package ec2

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ectypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/session"
	"github.com/requset700k/cledyu/api/internal/vmmetrics"
)

// fakeEC2는 ec2API 의 인메모리 가짜 구현이다. 코드가 쓰는 필터(tag:*, instance-state-name)만 해석한다.
type fakeEC2 struct {
	instances     []ectypes.Instance
	nextID        int
	runInputs     []*awsec2.RunInstancesInput
	terminated    []string
	createTagsErr error // 설정 시 CreateTags가 이 에러를 반환한다(부팅 기록 실패 경로 테스트용).
}

func (f *fakeEC2) RunInstances(_ context.Context, in *awsec2.RunInstancesInput, _ ...func(*awsec2.Options)) (*awsec2.RunInstancesOutput, error) {
	f.runInputs = append(f.runInputs, in)
	f.nextID++
	id := "i-" + strings.Repeat("0", 3) + itoa(f.nextID)
	inst := ectypes.Instance{
		InstanceId: aws.String(id),
		State:      &ectypes.InstanceState{Name: ectypes.InstanceStateNamePending},
	}
	if len(in.TagSpecifications) > 0 {
		inst.Tags = in.TagSpecifications[0].Tags
	}
	f.instances = append(f.instances, inst)
	return &awsec2.RunInstancesOutput{Instances: []ectypes.Instance{inst}}, nil
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *awsec2.DescribeInstancesInput, _ ...func(*awsec2.Options)) (*awsec2.DescribeInstancesOutput, error) {
	var matched []ectypes.Instance
	for _, inst := range f.instances {
		if matchesFilters(inst, in.Filters) {
			matched = append(matched, inst)
		}
	}
	return &awsec2.DescribeInstancesOutput{
		Reservations: []ectypes.Reservation{{Instances: matched}},
	}, nil
}

func (f *fakeEC2) CreateTags(_ context.Context, in *awsec2.CreateTagsInput, _ ...func(*awsec2.Options)) (*awsec2.CreateTagsOutput, error) {
	if f.createTagsErr != nil {
		return nil, f.createTagsErr
	}
	for _, id := range in.Resources {
		for i := range f.instances {
			if aws.ToString(f.instances[i].InstanceId) == id {
				f.instances[i].Tags = append(f.instances[i].Tags, in.Tags...)
			}
		}
	}
	return &awsec2.CreateTagsOutput{}, nil
}

func (f *fakeEC2) TerminateInstances(_ context.Context, in *awsec2.TerminateInstancesInput, _ ...func(*awsec2.Options)) (*awsec2.TerminateInstancesOutput, error) {
	for _, id := range in.InstanceIds {
		f.terminated = append(f.terminated, id)
		for i := range f.instances {
			if aws.ToString(f.instances[i].InstanceId) == id {
				f.instances[i].State = &ectypes.InstanceState{Name: ectypes.InstanceStateNameShuttingDown}
			}
		}
	}
	return &awsec2.TerminateInstancesOutput{}, nil
}

func matchesFilters(inst ectypes.Instance, filters []ectypes.Filter) bool {
	for _, flt := range filters {
		name := aws.ToString(flt.Name)
		switch {
		case name == "instance-state-name":
			if inst.State == nil || !contains(flt.Values, string(inst.State.Name)) {
				return false
			}
		case strings.HasPrefix(name, "tag:"):
			if !contains(flt.Values, tagValue(&inst, strings.TrimPrefix(name, "tag:"))) {
				return false
			}
		}
	}
	return true
}

func contains(vals []string, v string) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func testCfg() *config.AWSConfig {
	return &config.AWSConfig{
		Region:                "ap-northeast-2",
		LaunchTemplateID:      "lt-abc123",
		InstanceType:          "t3.medium",
		SessionTTLHours:       3,
		MaxActiveSessions:     5,
		TailnetHostnamePrefix: "lab",
		TailscaleAuthKey:      "tskey-auth-test",
	}
}

func TestCreate_TagsAndProvider(t *testing.T) {
	f := &fakeEC2{}
	p := newWithAPI(f, testCfg(), nil, nil)

	sess, err := p.Create(context.Background(), "abc123", "lab-k8s-basics", "user-1", session.BootInit{
		Packages: []string{"jq"},
		Runcmd:   []string{"echo hi"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Provider != session.ProviderEC2 {
		t.Errorf("provider = %q, want ec2", sess.Provider)
	}
	if sess.InstanceID == "" {
		t.Error("InstanceID empty")
	}
	if sess.Region != "ap-northeast-2" {
		t.Errorf("region = %q", sess.Region)
	}
	if sess.Status != "provisioning" {
		t.Errorf("status = %q, want provisioning", sess.Status)
	}
	// 세션 태그가 인스턴스에 붙었는지 확인.
	if got := tagValue(&f.instances[0], tagSessionID); got != "abc123" {
		t.Errorf("session-id tag = %q", got)
	}
	if got := tagValue(&f.instances[0], tagUserID); got != "user-1" {
		t.Errorf("user-id tag = %q", got)
	}
	// cloud-init user-data 에 tailscale up(authkey) 과 랩 runcmd 가 들어갔는지 확인.
	if len(f.runInputs) != 1 || f.runInputs[0].UserData == nil {
		t.Fatal("RunInstances UserData missing")
	}
	ud := decodeUserData(t, aws.ToString(f.runInputs[0].UserData))
	if !strings.Contains(ud, "tailscale up") || !strings.Contains(ud, "lab-abc123") {
		t.Errorf("user-data missing tailscale up/hostname:\n%s", ud)
	}
	if !strings.Contains(ud, "echo hi") {
		t.Errorf("user-data missing lab runcmd:\n%s", ud)
	}
}

func TestGet_NotFound(t *testing.T) {
	p := newWithAPI(&fakeEC2{}, testCfg(), nil, nil)
	_, err := p.Get(context.Background(), "missing")
	if !errors.Is(err, session.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestGet_AfterCreate(t *testing.T) {
	f := &fakeEC2{}
	p := newWithAPI(f, testCfg(), nil, nil)
	created, _ := p.Create(context.Background(), "s1", "lab-docker-basics", "u9", session.BootInit{})

	got, err := p.Get(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.InstanceID != created.InstanceID || got.LabID != "lab-docker-basics" || got.UserID != "u9" {
		t.Errorf("Get mismatch: %+v", got)
	}
}

func TestFindActiveByUser_And_Count(t *testing.T) {
	f := &fakeEC2{}
	p := newWithAPI(f, testCfg(), nil, nil)
	ctx := context.Background()
	_, _ = p.Create(ctx, "s1", "lab-a", "userA", session.BootInit{})
	_, _ = p.Create(ctx, "s2", "lab-b", "userB", session.BootInit{})

	id, err := p.FindActiveByUser(ctx, "userA")
	if err != nil || id != "s1" {
		t.Errorf("FindActiveByUser(userA) = %q, %v; want s1", id, err)
	}
	if id, _ := p.FindActiveByUser(ctx, "nobody"); id != "" {
		t.Errorf("FindActiveByUser(nobody) = %q, want empty", id)
	}
	n, err := p.CountActiveSessions(ctx)
	if err != nil || n != 2 {
		t.Errorf("CountActiveSessions = %d, %v; want 2", n, err)
	}
}

func TestDelete_Terminates(t *testing.T) {
	f := &fakeEC2{}
	p := newWithAPI(f, testCfg(), nil, nil)
	ctx := context.Background()
	created, _ := p.Create(ctx, "s1", "lab-a", "userA", session.BootInit{})

	if err := p.Delete(ctx, "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.terminated) != 1 || f.terminated[0] != created.InstanceID {
		t.Errorf("terminated = %v, want [%s]", f.terminated, created.InstanceID)
	}
	// 삭제 후엔 활성 목록에서 빠진다(shutting-down).
	if n, _ := p.CountActiveSessions(ctx); n != 0 {
		t.Errorf("active after delete = %d, want 0", n)
	}
	if err := p.Delete(ctx, "s1"); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
}

func TestReapExpiredSessions(t *testing.T) {
	f := &fakeEC2{}
	p := newWithAPI(f, testCfg(), nil, nil)
	ctx := context.Background()
	_, _ = p.Create(ctx, "fresh", "lab-a", "u1", session.BootInit{})

	// 만료된 세션을 직접 주입(expires-at 과거).
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	f.instances = append(f.instances, ectypes.Instance{
		InstanceId: aws.String("i-expired"),
		State:      &ectypes.InstanceState{Name: ectypes.InstanceStateNameRunning},
		Tags: []ectypes.Tag{
			{Key: aws.String(tagManagedBy), Value: aws.String(managedValue)},
			{Key: aws.String(tagSessionID), Value: aws.String("old")},
			{Key: aws.String(tagExpiresAt), Value: aws.String(past)},
		},
	})

	reaped, err := p.ReapExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("ReapExpiredSessions: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != "old" {
		t.Errorf("reaped = %v, want [old]", reaped)
	}
	if len(f.terminated) != 1 || f.terminated[0] != "i-expired" {
		t.Errorf("terminated = %v, want [i-expired]", f.terminated)
	}
}

func TestVMIAddress(t *testing.T) {
	f := &fakeEC2{}
	p := newWithAPI(f, testCfg(), nil, nil)
	ctx := context.Background()
	_, _ = p.Create(ctx, "s1", "lab-a", "u1", session.BootInit{})

	// pending 상태면 아직 도달 불가.
	if _, err := p.VMIAddress(ctx, "s1"); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("VMIAddress(pending) err = %v, want ErrNotFound", err)
	}
	// running 으로 전이시키면 MagicDNS 호스트네임 반환.
	f.instances[0].State = &ectypes.InstanceState{Name: ectypes.InstanceStateNameRunning}
	addr, err := p.VMIAddress(ctx, "s1")
	if err != nil || addr != "lab-s1" {
		t.Errorf("VMIAddress = %q, %v; want lab-s1", addr, err)
	}
}

// TestVMBootSuccessRecordedOnce_EC2는 리뷰 코멘트가 지적한 EC2 부팅 성공 미계측을 검증
// running 전이를 처음 관측한 Get() 호출에서 vm_boot_total{result=success,env=ec2}가 1회 기록,
// 이후 반복 폴링에서는 dedup 태그(tagBootResultRecorded)로 중복 집계되지 않아야 함
func TestVMBootSuccessRecordedOnce_EC2(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis 구동 실패: %v", err)
	}
	defer s.Close()

	// 2. 가짜 Redis 주소로 클라이언트 생성
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	reg := prometheus.NewRegistry()
	met := vmmetrics.New(reg)
	f := &fakeEC2{}

	p := newWithAPI(f, testCfg(), met, rdb)
	ctx := context.Background()
	_, _ = p.Create(ctx, "s1", "lab-a", "u1", session.BootInit{})
	f.instances[0].State = &ectypes.InstanceState{Name: ectypes.InstanceStateNameRunning}

	if _, err := p.Get(ctx, "s1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if c := testutil.CollectAndCount(met.Collector()); c != 1 {
		t.Errorf("성공 메트릭 샘플 수 = %d, want 1", c)
	}
	if got := testutil.ToFloat64(met.Collector().WithLabelValues(vmmetrics.ResultSuccess, session.ProviderEC2)); got != 1 {
		t.Errorf("success{env=ec2} = %v, want 1", got)
	}

	// 반복 폴링 — 중복 집계되면 안 된다.
	if _, err := p.Get(ctx, "s1"); err != nil {
		t.Fatalf("Get(재폴링): %v", err)
	}
	if c := testutil.CollectAndCount(met.Collector()); c != 1 {
		t.Errorf("중복 기록됨: 샘플 수 = %d, want 1", c)
	}
}

// TestReapStuckSessions_RecordsBootFailure_EC2는 timeout 안에 running이 되지 못해 회수되는
// EC2 인스턴스가 vm_boot_total{result=failed,env=ec2}로 기록되는지 검증
func TestReapStuckSessions_RecordsBootFailure_EC2(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis 구동 실패: %v", err)
	}
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	reg := prometheus.NewRegistry()
	met := vmmetrics.New(reg)
	f := &fakeEC2{}

	p := newWithAPI(f, testCfg(), met, rdb)
	ctx := context.Background()
	_, _ = p.Create(ctx, "stuck", "lab-a", "u1", session.BootInit{})
	// started-at 을 timeout 이전 과거로 되돌린다(여전히 pending 상태).
	past := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	for i := range f.instances[0].Tags {
		if aws.ToString(f.instances[0].Tags[i].Key) == tagStartedAt {
			f.instances[0].Tags[i].Value = aws.String(past)
		}
	}

	reaped, err := p.ReapStuckSessions(ctx, 5*time.Minute)
	if err != nil {
		t.Fatalf("ReapStuckSessions: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != "stuck" {
		t.Fatalf("reaped = %v, want [stuck]", reaped)
	}
	if got := testutil.ToFloat64(met.Collector().WithLabelValues(vmmetrics.ResultFailed, session.ProviderEC2)); got != 1 {
		t.Errorf("failed{env=ec2} = %v, want 1", got)
	}
}

// TestVMBootRecord_ReleasesLockOnCreateTagsFailure_EC2는 CreateTags 실패 시 동시성 락이
// 해제되어 다음 폴링이 부팅 결과를 재기록할 수 있는지 검증한다(락이 남으면 세션 TTL 내내 메트릭 영구 누락).
func TestVMBootRecord_ReleasesLockOnCreateTagsFailure_EC2(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis 구동 실패: %v", err)
	}
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	reg := prometheus.NewRegistry()
	met := vmmetrics.New(reg)
	f := &fakeEC2{createTagsErr: errors.New("throttled")}
	p := newWithAPI(f, testCfg(), met, rdb)

	inst := &ectypes.Instance{InstanceId: aws.String("i-boom")}
	lockKey := "cledyu:lock:vm_boot:i-boom"

	// 1) CreateTags 실패 → 메트릭 미기록 + 락은 해제돼 있어야 한다.
	p.recordBootOnce(context.Background(), inst, vmmetrics.ResultSuccess)
	if got := testutil.ToFloat64(met.Collector().WithLabelValues(vmmetrics.ResultSuccess, session.ProviderEC2)); got != 0 {
		t.Fatalf("실패 경로에서 기록됨: success{env=ec2} = %v, want 0", got)
	}
	if s.Exists(lockKey) {
		t.Fatalf("CreateTags 실패 후에도 락 %q 이 남아 있음 — 재기록이 영구 차단됨", lockKey)
	}

	// 2) CreateTags 복구 후 재폴링 → 정확히 1회 기록돼야 한다.
	f.createTagsErr = nil
	p.recordBootOnce(context.Background(), inst, vmmetrics.ResultSuccess)
	if got := testutil.ToFloat64(met.Collector().WithLabelValues(vmmetrics.ResultSuccess, session.ProviderEC2)); got != 1 {
		t.Errorf("복구 후 success{env=ec2} = %v, want 1", got)
	}
}

func decodeUserData(t *testing.T, b64 string) string {
	t.Helper()
	dec, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode user-data: %v", err)
	}
	return string(dec)
}
