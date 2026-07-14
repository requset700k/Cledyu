// Package ec2는 AWS EC2 오버플로우 세션 프로비저너다 — 온프렘 KubeVirt 풀이 가득 찼을 때
// 학습 세션을 EC2 인스턴스로 버스트한다. session.Provider 를 구현해 KubeVirt 매니저와
// 동일한 계약으로 디스패처에 끼워진다.
//
// 세션 모델은 KubeVirt 의 namespace(annotation) 모델을 EC2 인스턴스 태그로 대응한다:
// 인스턴스에 cledyu.io/* 태그를 붙여 세션 ID·소유자·만료 시각을 보관하고, 조회/회수는
// 태그 필터로 수행한다. base64 cloud-init user-data 로 tailnet 가입·랩 초기화를 한다.
package ec2

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ectypes "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/requset700k/cledyu/api/internal/config"
	"github.com/requset700k/cledyu/api/internal/session"
	"github.com/requset700k/cledyu/api/internal/vmmetrics"
)

// 세션 인스턴스 식별·메타데이터 태그. KubeVirt 의 namespace 라벨/annotation 과 의미가 1:1 대응한다.
const (
	tagManagedBy          = "cledyu.io/managed-by"
	managedValue          = "cledyu-session"
	tagSessionID          = "cledyu.io/session-id"
	tagUserID             = "cledyu.io/user-id"
	tagLabID              = "cledyu.io/lab-id"
	tagStartedAt          = "cledyu.io/started-at"
	tagExpiresAt          = "cledyu.io/expires-at"
	tagBootResultRecorded = "cledyu.io/boot-result-recorded"
)

// ec2API는 프로비저너가 쓰는 EC2 호출 표면이다. 단위 테스트에서 가짜 구현으로 대체한다.
type ec2API interface {
	RunInstances(context.Context, *awsec2.RunInstancesInput, ...func(*awsec2.Options)) (*awsec2.RunInstancesOutput, error)
	DescribeInstances(context.Context, *awsec2.DescribeInstancesInput, ...func(*awsec2.Options)) (*awsec2.DescribeInstancesOutput, error)
	TerminateInstances(context.Context, *awsec2.TerminateInstancesInput, ...func(*awsec2.Options)) (*awsec2.TerminateInstancesOutput, error)
	CreateTags(context.Context, *awsec2.CreateTagsInput, ...func(*awsec2.Options)) (*awsec2.CreateTagsOutput, error)
}

// Provisioner는 EC2 세션 수명주기를 관리한다. session.Provider 를 구현한다.
type Provisioner struct {
	api    ec2API
	cfg    *config.AWSConfig
	met    *vmmetrics.Recorder
	rdb    *redis.Client
	log    *zap.Logger
	minter KeyMinter // nil이면 정적 cfg.TailscaleAuthKey 폴백(하위호환)
}

// Provisioner가 프로바이더 중립 계약을 구현함을 컴파일 타임에 보장한다.
var _ session.Provider = (*Provisioner)(nil)

// NewProvisioner는 표준 AWS SDK 자격증명 체인(환경변수 AWS_ACCESS_KEY_ID/SECRET 등 —
// Vault→ESO 주입)으로 EC2 클라이언트를 만든다. region 은 AWSConfig 값을 우선한다.
// met는 kubevirt 매니저와 공유하는 vm_boot_total Recorder다(nil이면 기록을 건너뛴다).
func NewProvisioner(ctx context.Context, cfg *config.AWSConfig, met *vmmetrics.Recorder, rdb *redis.Client, log *zap.Logger) (*Provisioner, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("ec2 provisioner: load aws config: %w", err)
	}
	p := &Provisioner{api: awsec2.NewFromConfig(awsCfg), cfg: cfg, met: met, rdb: rdb, log: log}
	// Tailscale API 키가 있으면 세션별 one-off authkey 를 동적 발급한다(issue #307). 없으면 정적
	// cfg.TailscaleAuthKey 폴백(하위호환).
	if cfg.TailscaleAPIKey != "" {
		ttl := time.Duration(cfg.SessionKeyTTLSeconds) * time.Second
		if ttl <= 0 {
			ttl = 10 * time.Minute
		}
		tag := cfg.SessionKeyTag
		if tag == "" {
			tag = "tag:lab-ec2"
		}
		p.minter = newTailscaleKeyMinter(cfg.TailscaleAPIKey, "-", tag, ttl)
	}
	return p, nil
}

// newWithAPI는 주입된 EC2 API 로 프로비저너를 만든다(테스트 전용).
func newWithAPI(api ec2API, cfg *config.AWSConfig, met *vmmetrics.Recorder, rdb *redis.Client) *Provisioner {
	return &Provisioner{api: api, cfg: cfg, met: met, rdb: rdb}
}

// resolveSessionAuthKey는 세션 cloud-init 에 넣을 tailnet authkey 를 결정한다. minter 가 있으면
// 세션마다 one-off 키를 발급하고, 발급 실패 시 정적 reusable 키로 폴백하지 않고 빈 값을 반환한다
// (fail-secure: 유출 위험 있는 정적 키 대신 그 세션만 터미널 비활성, SSM 채점은 유지). minter 가
// 없으면 정적 cfg.TailscaleAuthKey(하위호환).
func (p *Provisioner) resolveSessionAuthKey(ctx context.Context) string {
	if p.minter == nil {
		return p.cfg.TailscaleAuthKey
	}
	key, err := p.minter.Mint(ctx)
	if err != nil {
		if p.log != nil {
			p.log.Warn("세션 tailnet authkey 발급 실패 — 터미널 없이 부팅(SSM 채점만 동작)", zap.Error(err))
		}
		return ""
	}
	return key
}

func (p *Provisioner) Create(ctx context.Context, sessionID, labID, userID string, init session.BootInit) (*session.Session, error) {
	now := time.Now().UTC()
	expires := now.Add(time.Duration(p.cfg.SessionTTLHours) * time.Hour)

	userData := base64.StdEncoding.EncodeToString([]byte(renderCloudInit(sessionID, p.cfg, init, p.resolveSessionAuthKey(ctx))))

	in := &awsec2.RunInstancesInput{
		LaunchTemplate: &ectypes.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(p.cfg.LaunchTemplateID),
			// $Latest 를 명시한다 — 생략 시 템플릿 default 버전이 쓰여, terraform 이 AMI/SG/
			// user-data 변경으로 새 버전을 만들어도(default 갱신 없이는) stale 템플릿으로 계속 기동한다.
			Version: aws.String("$Latest"),
		},
		MinCount: aws.Int32(1),
		MaxCount: aws.Int32(1),
		UserData: aws.String(userData),
		TagSpecifications: []ectypes.TagSpecification{{
			ResourceType: ectypes.ResourceTypeInstance,
			Tags: []ectypes.Tag{
				{Key: aws.String(tagManagedBy), Value: aws.String(managedValue)},
				{Key: aws.String(tagSessionID), Value: aws.String(sessionID)},
				{Key: aws.String(tagUserID), Value: aws.String(userID)},
				{Key: aws.String(tagLabID), Value: aws.String(labID)},
				{Key: aws.String(tagStartedAt), Value: aws.String(now.Format(time.RFC3339))},
				{Key: aws.String(tagExpiresAt), Value: aws.String(expires.Format(time.RFC3339))},
				{Key: aws.String("Name"), Value: aws.String(tailnetHostname(p.cfg, sessionID))},
			},
		}},
	}
	// 인스턴스 타입 오버라이드(빈 값이면 Launch Template 기본값 사용).
	if p.cfg.InstanceType != "" {
		in.InstanceType = ectypes.InstanceType(p.cfg.InstanceType)
	}

	out, err := p.api.RunInstances(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("run instance: %w", err)
	}
	if len(out.Instances) == 0 {
		return nil, fmt.Errorf("run instance: no instance returned")
	}

	return &session.Session{
		ID:         sessionID,
		LabID:      labID,
		UserID:     userID,
		Status:     "provisioning",
		StartedAt:  now,
		ExpiresAt:  expires,
		Provider:   session.ProviderEC2,
		InstanceID: aws.ToString(out.Instances[0].InstanceId),
		Region:     p.cfg.Region,
	}, nil
}

func (p *Provisioner) Get(ctx context.Context, sessionID string) (*session.Session, error) {
	inst, err := p.findActiveInstance(ctx, tagFilter(tagSessionID, sessionID))
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, session.ErrNotFound
	}
	sess := instanceToSession(inst, p.cfg.Region)
	// KubeVirt Get()의 ready-at 최초 관측 시점 기록과 대응
	// running 전이를 처음 본 폴링에서 vm_boot_total{result=success,env=ec2}를 1회 기록
	if sess.Status == "ready" {
		p.recordBootOnce(ctx, inst, vmmetrics.ResultSuccess)
	}
	return sess, nil
}

func (p *Provisioner) Delete(ctx context.Context, sessionID string) error {
	inst, err := p.findActiveInstance(ctx, tagFilter(tagSessionID, sessionID))
	if err != nil {
		return err
	}
	if inst == nil {
		return session.ErrNotFound
	}
	return p.terminate(ctx, aws.ToString(inst.InstanceId))
}

func (p *Provisioner) FindActiveByUser(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", nil
	}
	inst, err := p.findActiveInstance(ctx, tagFilter(tagUserID, userID))
	if err != nil {
		return "", err
	}
	if inst == nil {
		return "", nil
	}
	return tagValue(inst, tagSessionID), nil
}

// Capacity는 EC2 오버플로우 동시 세션 상한(AWS.MaxActiveSessions)을 반환한다.
func (p *Provisioner) Capacity() int { return p.cfg.MaxActiveSessions }

func (p *Provisioner) CountActiveSessions(ctx context.Context) (int, error) {
	insts, err := p.listActive(ctx)
	if err != nil {
		return 0, err
	}
	return len(insts), nil
}

// ReapStuckSessions는 timeout 내 running 이 되지 못한(아직 pending) 인스턴스를 terminate 한다.
// running 인스턴스는 정상 세션이므로 회수하지 않는다(KubeVirt ReapStuckSessions 와 동일 의미).
func (p *Provisioner) ReapStuckSessions(ctx context.Context, timeout time.Duration) ([]string, error) {
	insts, err := p.listActive(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var reaped []string
	for i := range insts {
		inst := &insts[i]
		if inst.State != nil && inst.State.Name == ectypes.InstanceStateNameRunning {
			continue // ready 세션 보호
		}
		started, perr := time.Parse(time.RFC3339, tagValue(inst, tagStartedAt))
		if perr != nil {
			continue
		}
		if now.Sub(started) < timeout {
			continue
		}
		// KubeVirt provisioningTimedOut과 대응되는 EC2 부팅 실패 케이스: timeout 안에 running이
		// 되지 못해 회수되는 인스턴스를 vm_boot_total{result=failed,env=ec2}로 기록한다.
		// 주의(범위 문서화): 부트스트랩 스크립트 실패로 인스턴스가 이 timeout 이전에 스스로
		// shutting-down/terminated 상태가 되는 경우는 activeStateFilter(pending/running)에 걸려
		// 이 루프에 잡히지 않으므로 현재 미계측이다 — timeout 경로만 커버한다.
		p.recordBootOnce(ctx, inst, vmmetrics.ResultFailed)
		if err := p.terminate(ctx, aws.ToString(inst.InstanceId)); err != nil {
			continue // best-effort — 다음 주기 재시도
		}
		reaped = append(reaped, tagValue(inst, tagSessionID))
	}
	return reaped, nil
}

// ReapExpiredSessions는 expires_at(세션 TTL)이 지난 인스턴스를 running 여부와 무관하게 terminate 한다.
// EC2 비용 누수(만료된 세션 인스턴스가 계속 과금되는 것)를 막는 핵심 가드레일이다.
func (p *Provisioner) ReapExpiredSessions(ctx context.Context) ([]string, error) {
	insts, err := p.listActive(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var reaped []string
	for i := range insts {
		inst := &insts[i]
		expires, perr := time.Parse(time.RFC3339, tagValue(inst, tagExpiresAt))
		if perr != nil {
			continue // 만료 시각을 못 읽으면 보수적으로 건너뛴다(stuck reaper 가 처리)
		}
		if now.Before(expires) {
			continue
		}
		if err := p.terminate(ctx, aws.ToString(inst.InstanceId)); err != nil {
			continue
		}
		reaped = append(reaped, tagValue(inst, tagSessionID))
	}
	return reaped, nil
}

// VMIAddress는 세션 인스턴스의 tailnet MagicDNS 호스트네임을 반환한다(라이브 터미널/IDE 프록시용).
// 인스턴스가 아직 running 이 아니거나 tailnet 미가입(authkey 미설정)이면 ErrNotFound.
func (p *Provisioner) VMIAddress(ctx context.Context, sessionID string) (string, error) {
	if p.cfg.TailscaleAuthKey == "" {
		return "", session.ErrNotFound // tailnet 미사용 — 도달 주소 없음
	}
	inst, err := p.findActiveInstance(ctx, tagFilter(tagSessionID, sessionID))
	if err != nil {
		return "", err
	}
	if inst == nil || inst.State == nil || inst.State.Name != ectypes.InstanceStateNameRunning {
		return "", session.ErrNotFound
	}
	return tailnetHostname(p.cfg, sessionID), nil
}

// --- 내부 헬퍼 ---

// bootRecordLockTTL은 recordBootOnce 동시성 락의 수명이다. 영구 dedup 은 tagBootResultRecorded
// 태그가 담당하므로, 이 락은 CreateTags+Inc 구간과 EC2 태그 전파(eventual consistency) 지연만
// 덮으면 된다. 세션 TTL(수 시간)보다 훨씬 짧게 잡아, ctx 취소 등으로 해제에 실패해도 곧 만료돼
// 다음 폴링에서 부팅 결과를 재기록할 수 있게 한다.
const bootRecordLockTTL = 5 * time.Minute

// recordBootOnce는 inst에 대해 vm_boot_total을 최대 1회만 기록한다.
// dedup 토큰은 tagBootResultRecorded 태그다 — KubeVirt가 namespace annotation +
// Update()를 dedup 토큰으로 쓰는 것과 같은 원리로, CreateTags 성공 후에만 Inc를 호출해
// 여러 API 레플리카의 동시 폴링으로 인한 중복 집계를 막는다.
func (p *Provisioner) recordBootOnce(ctx context.Context, inst *ectypes.Instance, result string) {
	if p.met == nil || p.rdb == nil || tagValue(inst, tagBootResultRecorded) != "" {
		return
	}

	instanceID := aws.ToString(inst.InstanceId)
	lockKey := fmt.Sprintf("cledyu:lock:vm_boot:%s", instanceID)

	ok, err := p.rdb.SetNX(ctx, lockKey, "true", bootRecordLockTTL).Result()
	if err != nil || !ok {
		// Redis 통신 장애가 났거나 다른 레플리카가 이미 키를 선점했다면 중복 가산 방지를 위해 즉시 리턴
		return
	}

	_, err = p.api.CreateTags(ctx, &awsec2.CreateTagsInput{
		Resources: []string{aws.ToString(inst.InstanceId)},
		Tags: []ectypes.Tag{
			{Key: aws.String(tagBootResultRecorded), Value: aws.String("true")},
		},
	})
	if err != nil {
		// ctx가 취소/만료돼 CreateTags가 실패한 경우, 같은 ctx 로는 Del 도 실행되지 않아 락이
		// TTL 까지 남는다. 짧은 timeout 의 background ctx 로 즉시 해제해, 다음 폴링이 부팅
		// 결과를 재기록할 수 있게 한다(bootRecordLockTTL 만료를 기다리지 않도록).
		relCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = p.rdb.Del(relCtx, lockKey).Result()
		cancel()
		return
	}
	p.met.RecordBoot(result, session.ProviderEC2)
	reason := vmmetrics.LabReasonReady
	if result == vmmetrics.ResultFailed {
		reason = vmmetrics.LabReasonTimeout
	}
	duration := -1.0
	if started, err := time.Parse(time.RFC3339, tagValue(inst, tagStartedAt)); err == nil {
		duration = time.Since(started).Seconds()
	}
	p.met.RecordLabStart(result, vmmetrics.LabEnvEC2, reason, duration)
}

// activeStateFilter는 종료된(terminated/shutting-down/stopping/stopped) 인스턴스를 제외하고
// 활성(pending/running) 인스턴스만 남기는 필터다.
func activeStateFilter() ectypes.Filter {
	return ectypes.Filter{
		Name:   aws.String("instance-state-name"),
		Values: []string{"pending", "running"},
	}
}

func tagFilter(key, value string) ectypes.Filter {
	return ectypes.Filter{Name: aws.String("tag:" + key), Values: []string{value}}
}

func managedFilter() ectypes.Filter {
	return tagFilter(tagManagedBy, managedValue)
}

// listActive는 cledyu 세션 태그가 붙은 활성 인스턴스 전체를 반환한다.
func (p *Provisioner) listActive(ctx context.Context) ([]ectypes.Instance, error) {
	return p.describe(ctx, managedFilter(), activeStateFilter())
}

// findActiveInstance는 주어진 태그 필터에 더해 managed-by·활성 상태로 한정해 첫 인스턴스를 반환한다.
// 없으면 (nil, nil).
func (p *Provisioner) findActiveInstance(ctx context.Context, extra ectypes.Filter) (*ectypes.Instance, error) {
	insts, err := p.describe(ctx, managedFilter(), activeStateFilter(), extra)
	if err != nil {
		return nil, err
	}
	if len(insts) == 0 {
		return nil, nil
	}
	return &insts[0], nil
}

func (p *Provisioner) describe(ctx context.Context, filters ...ectypes.Filter) ([]ectypes.Instance, error) {
	var out []ectypes.Instance
	paginator := awsec2.NewDescribeInstancesPaginator(p.api, &awsec2.DescribeInstancesInput{Filters: filters})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe instances: %w", err)
		}
		for _, r := range page.Reservations {
			out = append(out, r.Instances...)
		}
	}
	return out, nil
}

func (p *Provisioner) terminate(ctx context.Context, instanceID string) error {
	_, err := p.api.TerminateInstances(ctx, &awsec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return fmt.Errorf("terminate instance %s: %w", instanceID, err)
	}
	return nil
}

// instanceToSession은 EC2 인스턴스(태그·상태)를 프로바이더 중립 Session 으로 변환한다.
func instanceToSession(inst *ectypes.Instance, region string) *session.Session {
	startedAt, _ := time.Parse(time.RFC3339, tagValue(inst, tagStartedAt))
	expiresAt, _ := time.Parse(time.RFC3339, tagValue(inst, tagExpiresAt))
	return &session.Session{
		ID:         tagValue(inst, tagSessionID),
		LabID:      tagValue(inst, tagLabID),
		UserID:     tagValue(inst, tagUserID),
		Status:     instanceStatus(inst),
		StartedAt:  startedAt,
		ExpiresAt:  expiresAt,
		Provider:   session.ProviderEC2,
		InstanceID: aws.ToString(inst.InstanceId),
		Region:     region,
	}
}

// instanceStatus는 EC2 인스턴스 상태를 세션 상태(provisioning|ready|failed)로 매핑한다.
func instanceStatus(inst *ectypes.Instance) string {
	if inst.State == nil {
		return "provisioning"
	}
	switch inst.State.Name {
	case ectypes.InstanceStateNameRunning:
		return "ready"
	case ectypes.InstanceStateNamePending:
		return "provisioning"
	default:
		return "failed"
	}
}

func tagValue(inst *ectypes.Instance, key string) string {
	for _, t := range inst.Tags {
		if aws.ToString(t.Key) == key {
			return aws.ToString(t.Value)
		}
	}
	return ""
}
