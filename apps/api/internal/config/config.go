// 우선순위: 환경변수 (CLEDYU_*) > config.yaml > 기본값.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
	"golang.org/x/crypto/ssh"
)

type Config struct {
	Server      ServerConfig    `mapstructure:"server"`
	Redis       RedisConfig     `mapstructure:"redis"`
	Keycloak    KeycloakConfig  `mapstructure:"keycloak"`
	KubeVirt    KubeVirtConfig  `mapstructure:"kubevirt"`
	AWS         AWSConfig       `mapstructure:"aws"`
	Kafka       KafkaConfig     `mapstructure:"kafka"`
	AI          AIConfig        `mapstructure:"ai"`
	DB          DBConfig        `mapstructure:"db"`
	Analytics   AnalyticsConfig `mapstructure:"analytics"`
	OTel        OTelConfig      `mapstructure:"otel"`
	FrontendURL string          `mapstructure:"frontend_url"`
}

// AnalyticsConfig는 D3 강사 분석용 BigQuery 설정이다.
// ProjectID 가 비면 비활성 — 강사 분석 핸들러가 503 을 반환한다.
type AnalyticsConfig struct {
	ProjectID string `mapstructure:"project_id"` // env: CLEDYU_ANALYTICS_PROJECT_ID
	Dataset   string `mapstructure:"dataset"`    // env: CLEDYU_ANALYTICS_DATASET
}

// DBConfig는 PostgreSQL 영속 계층 설정이다.
// DSN 이 비면 영속화 비활성 — 진행 상태는 in-memory 전용으로 동작한다(로컬/CI).
// DSN 에 비밀번호가 포함되므로 값 전체를 Secret(ESO: cledyu-api-db)으로 주입한다.
type DBConfig struct {
	DSN string `mapstructure:"dsn"` // 예: postgres://cledyu:***@cledyu-pg-rw.postgres.svc:5432/cledyu?sslmode=require
}

// AIConfig는 AI 학습 도우미 BFF(apps/ai-tutor) 연동 설정이다.
// BaseURL이 비면 AI 힌트는 비활성 — 핸들러가 Lab DSL의 정적 hint_levels로 폴백한다.
type AIConfig struct {
	BaseURL string `mapstructure:"base_url"` // 예: http://ai-tutor.ai-tutor.svc:8080
	// TimeoutSeconds: BFF 호출 타임아웃. BFF 내부의 Gemini 티어링(최대 3개 모델 순차 시도)을 감안해 둔다.
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
}

// KafkaConfig는 validation-requests 토픽 발행용 Kafka 연결 설정이다.
// 인증서 경로의 파일이 없으면(로컬/CI) publisher가 비활성화되고 검증은 mock으로 동작한다.
type KafkaConfig struct {
	Brokers string `mapstructure:"brokers"` // 쉼표로 구분된 broker 주소 목록
	Topic   string `mapstructure:"topic"`   // 검증 요청 발행 토픽(validation-requests)
	TLSCert string `mapstructure:"tls_cert"`
	TLSKey  string `mapstructure:"tls_key"`
	TLSCA   string `mapstructure:"tls_ca"`

	// 검증 결과 소비(consumer) 설정. ResultsTopic을 구독해 stepStore를 실제 결과로 갱신한다.
	ResultsTopic  string `mapstructure:"results_topic"`
	ConsumerGroup string `mapstructure:"consumer_group"`

	// EventsTopic: 학습 이벤트(lab_started 등) 발행 토픽. 학습 분석 파이프라인의 입력이다.
	EventsTopic string `mapstructure:"events_topic"`
}

type KubeVirtConfig struct {
	// Enabled: 세션 실행 백엔드(KubeVirt) 사용 여부. 기본 true(현행 온프렘 동작 보존).
	// false면 kubevirt manager 를 만들지 않아(main.go) 세션 Provider 가 nil → 세션 API 가 처음부터 503.
	// DR(EKS)처럼 KubeVirt CRD/오퍼레이터가 없는 환경에서 세션 생성이 lab-* ns/Secret 을 만든 뒤
	// VM 생성서 500 나며 네임스페이스가 고아로 남는 것을 차단한다. env: CLEDYU_KUBEVIRT_ENABLED.
	Enabled         bool   `mapstructure:"enabled"`
	Kubeconfig      string `mapstructure:"kubeconfig"`
	BaseImageNS     string `mapstructure:"base_image_ns"`
	BaseImageName   string `mapstructure:"base_image_name"`
	SessionTTLHours int    `mapstructure:"session_ttl_hours"`
	// StorageClass: 세션 VM 루트 디스크(베이스 PVC 클론)에 쓸 StorageClass.
	// lab 디스크는 ephemeral이라 replica 2 전용 SC(longhorn-r2)를 기본값으로 둔다.
	StorageClass string `mapstructure:"storage_class"`
	// LabSSHPublicKey: 세션 VM cloud-init이 user "lab"의 authorized_keys로 넣는 공개키.
	// 검증엔진이 이 키의 private 짝으로 virtctl ssh 접속한다. 비면 키를 넣지 않는다(비번/시리얼만).
	LabSSHPublicKey string `mapstructure:"lab_ssh_public_key"`
	// FileListSSHPublicKey: Session API 파일 목록 전용 제한 공개키.
	// 비면 cloud-init에 파일 목록 forced command와 key를 넣지 않아 기능이 비활성화된다.
	FileListSSHPublicKey string `mapstructure:"file_list_ssh_public_key"`
	// FileListSSHPublicKeyPath: 위 공개키를 Secret volume 파일에서 읽는 optional 경로.
	// env 주입은 Secret 생성 이후 running 프로세스에 반영되지 않으므로 k8s 배포에서는 파일 경로를 쓴다.
	FileListSSHPublicKeyPath string `mapstructure:"file_list_ssh_public_key_path"`
	// FileListSSHPrivateKeyPath: 위 공개키와 짝인 private key의 optional Secret mount 경로.
	FileListSSHPrivateKeyPath string `mapstructure:"file_list_ssh_private_key_path"`
	// ProvisionTimeoutMinutes: 세션 VM이 이 시간 내 VMI Running 이 안 되면 Get은 failed로 표시하고 reaper가 세션을 회수(삭제)한다.
	// stuck provisioning이 CDI 클론 재시도 thrash로 번지는 것을 차단. 0이면 비활성.
	ProvisionTimeoutMinutes int `mapstructure:"provision_timeout_minutes"`
	// MaxActiveSessions: 클러스터 전체 동시 활성 세션 상한. 초과 시 세션 생성을 429로 거부한다.
	// 스토리지/컴퓨트 용량을 넘어서는 무한정 세션 생성으로 Longhorn이 마르는 것을 방지. 0이면 무제한.
	MaxActiveSessions int `mapstructure:"max_active_sessions"`
}

// AWSConfig는 EC2 오버플로우(온프렘 KubeVirt 풀이 가득 찼을 때 AWS EC2로 버스트) 설정이다.
// Region·LaunchTemplateID·MaxActiveSessions 가 모두 채워져야 EC2 오버플로우가 활성화된다.
// 미설정(기본값)이면 EC2 비활성 — 세션은 KubeVirt 전용으로 동작한다(현행 동작 보존).
// AWS 자격증명은 표준 SDK 환경변수(AWS_ACCESS_KEY_ID/SECRET/REGION)로 주입한다 — 최소권한
// IAM 키를 Vault→External Secrets 로 컨테이너 env 에 넣는다(코드에 키를 두지 않는다).
type AWSConfig struct {
	Region           string `mapstructure:"region"`             // EC2 리전(예: ap-northeast-2)
	LaunchTemplateID string `mapstructure:"launch_template_id"` // 세션 VM 베이스 Launch Template(W1 terraform 산출물)
	InstanceType     string `mapstructure:"instance_type"`      // 인스턴스 타입 오버라이드. 빈 값이면 LT 기본값 사용.
	SessionTTLHours  int    `mapstructure:"session_ttl_hours"`  // EC2 세션 TTL. reaper가 만료 인스턴스를 terminate.
	// ProvisionTimeoutMinutes: EC2 인스턴스가 이 시간 내 running 이 안 되면 reaper가 terminate. 0이면 비활성.
	ProvisionTimeoutMinutes int `mapstructure:"provision_timeout_minutes"`
	// MaxActiveSessions: EC2 동시 세션 상한(오버플로우 용량). 0이면 EC2 오버플로우 비활성.
	MaxActiveSessions int `mapstructure:"max_active_sessions"`
	// TailnetHostnamePrefix: 세션 인스턴스가 tailscale up 시 쓰는 MagicDNS 호스트네임 prefix.
	// API/검증엔진은 "<prefix>-<sessionID>" 로 인스턴스에 도달한다(라이브 터미널/IDE 프록시·SSH).
	TailnetHostnamePrefix string `mapstructure:"tailnet_hostname_prefix"`
	// TailscaleAuthKey: 세션 인스턴스 cloud-init 이 tailnet 에 가입할 때 쓰는 ephemeral authkey.
	// Vault→ESO 로 주입. 비면 cloud-init 이 tailscale 가입을 생략(라이브 터미널/IDE 불가, SSM 채점만).
	// TailscaleAPIKey 가 설정되면 이 정적 키 대신 세션별 동적발급 키가 쓰인다(폴백용).
	TailscaleAuthKey string `mapstructure:"tailscale_auth_key"`
	// TailscaleAPIKey: 세션마다 one-off authkey 를 동적 발급하기 위한 Tailscale API 액세스 토큰
	// (auth_keys write 스코프 + tag:lab-ec2 발급 권한). 설정 시 프로비저너가 세션마다 비재사용·
	// ephemeral·짧은만료 키를 발급해 user-data 에 넣어, 정적 reusable 키가 세션(sudo lab 계정)으로
	// 유출되는 위험을 없앤다(issue #307). Vault→ESO 주입. 미설정 시 TailscaleAuthKey 폴백.
	//
	// 이 값이 OAuth **client secret**(권장, tag 스코프)이면 TailscaleOAuthClientID 도 함께 설정한다.
	// 그러면 프로비저너가 client_credentials 로 짧은수명(1h) 액세스 토큰을 교환·자동갱신해 쓴다.
	// TailscaleOAuthClientID 가 비면 이 값을 API 액세스 토큰으로 보고 직접 Bearer 로 쓴다.
	TailscaleAPIKey string `mapstructure:"tailscale_api_key"`
	// TailscaleOAuthClientID: TailscaleAPIKey 가 OAuth client secret 일 때의 client id. 설정되면
	// 발급 요청 전 /api/v2/oauth/token 에서 액세스 토큰을 교환(자동 갱신)한다. OAuth 액세스 토큰은
	// 1시간 만료라 정적으로 baked 하면 배포 1시간 뒤 발급이 끊기므로, 교환 방식이어야 지속 동작한다.
	TailscaleOAuthClientID string `mapstructure:"tailscale_oauth_client_id"`
	// SessionKeyTTLSeconds: 동적 발급 세션 authkey 의 만료(초). 인스턴스 부팅+가입 시간만 커버하면
	// 되므로 짧게 둔다. 기본 600(10분).
	SessionKeyTTLSeconds int `mapstructure:"session_key_ttl_seconds"`
	// SessionKeyTag: 동적 발급 세션 authkey 에 붙일 태그. 기본 tag:lab-ec2.
	SessionKeyTag string `mapstructure:"session_key_tag"`
	// APITailscaleAuthKey: api 파드 자신이 tsnet 으로 tailnet 에 가입할 때 쓰는 authkey(tag:cledyu-api).
	// 세션 authkey(TailscaleAuthKey)와 별개다 — api 가 EC2 세션에 라이브 터미널 SSH 를 붙이려면 자신이
	// tailnet 노드로 붙어야 한다(클러스터 파드는 tailnet/MagicDNS 에 직접 못 닿음). 비면 tsnet 미기동.
	APITailscaleAuthKey string `mapstructure:"api_tailscale_auth_key"`
	// APITailnetStateDir: tsnet 상태 디렉터리(쓰기 가능해야 함). 비면 tsnet 기본 경로. k8s 는 emptyDir 권장.
	APITailnetStateDir string `mapstructure:"api_tailnet_state_dir"`
	// LiveTerminalSSHUser/Password: api 가 EC2 세션에 라이브 터미널(SSH PTY)을 붙일 때 쓰는 계정.
	// cloud-init 이 만드는 lab 계정(기본 lab/lab)과 일치한다. tailnet 경유로만 도달 가능하며,
	// Tailscale SSH(accept) 면 none 인증이 먼저 통과하고, 일반 sshd 면 이 비밀번호로 폴백한다.
	LiveTerminalSSHUser     string `mapstructure:"live_terminal_ssh_user"`
	LiveTerminalSSHPassword string `mapstructure:"live_terminal_ssh_password"`
}

type KeycloakConfig struct {
	URL          string `mapstructure:"url"`
	Realm        string `mapstructure:"realm"`
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	RedirectURI  string `mapstructure:"redirect_uri"`
	CookieDomain string `mapstructure:"cookie_domain"`
	// AdminClientID/Secret: 관리자 유저 관리(역할 승격)용 service-account 클라이언트.
	// realm-management 의 manage-users·view-realm 역할이 부여된 confidential client 여야 한다
	// (런북 learner-auth.md §6.2). 비면 역할 승격 API 가 비활성(501)된다.
	AdminClientID     string `mapstructure:"admin_client_id"`
	AdminClientSecret string `mapstructure:"admin_client_secret"`
}

type ServerConfig struct {
	Addr string `mapstructure:"addr"`
	Mode string `mapstructure:"mode"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type OTelConfig struct {
	Endpoint string `mapstructure:"endpoint"`
}

func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.SetEnvPrefix("CLEDYU")
	// "." → "_" 변환으로 중첩 키를 env로 자동 매핑 (keycloak.client_secret → CLEDYU_KEYCLOAK_CLIENT_SECRET).
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("server.addr", ":8080")
	v.SetDefault("server.mode", "debug")
	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("keycloak.url", "https://keycloak.cledyu.local")
	// 학습자 realm(운영자 cledyu realm과 분리). client_secret은 환경변수
	// CLEDYU_KEYCLOAK_CLIENT_SECRET 로 주입(web=confidential BFF client).
	v.SetDefault("keycloak.realm", "cledyu-learn")
	v.SetDefault("keycloak.client_id", "web")
	v.SetDefault("keycloak.client_secret", "")
	v.SetDefault("keycloak.redirect_uri", "https://api.cledyu.local/api/v1/auth/callback")
	v.SetDefault("frontend_url", "https://app.cledyu.local")
	v.SetDefault("keycloak.cookie_domain", ".cledyu.local")
	// 역할 승격 service-account — 빈 기본값. env CLEDYU_KEYCLOAK_ADMIN_CLIENT_ID/SECRET 로 주입.
	v.SetDefault("keycloak.admin_client_id", "")
	v.SetDefault("keycloak.admin_client_secret", "")
	v.SetDefault("kubevirt.enabled", true) // 기본 활성(온프렘). DR 은 CLEDYU_KUBEVIRT_ENABLED=false 로 끈다.
	v.SetDefault("kubevirt.kubeconfig", os.Getenv("KUBECONFIG"))
	v.SetDefault("kubevirt.base_image_ns", "kubevirt")
	v.SetDefault("kubevirt.base_image_name", "ubuntu-2204-base")
	v.SetDefault("kubevirt.lab_ssh_public_key", "") // 빈 기본값 — env CLEDYU_KUBEVIRT_LAB_SSH_PUBLIC_KEY로 주입
	v.SetDefault("kubevirt.file_list_ssh_public_key", "")
	v.SetDefault("kubevirt.file_list_ssh_public_key_path", "/etc/vm-file-ssh/public_key")
	v.SetDefault("kubevirt.file_list_ssh_private_key_path", "/etc/vm-file-ssh/id_ed25519")
	v.SetDefault("kubevirt.storage_class", "longhorn-r2")
	v.SetDefault("kubevirt.session_ttl_hours", 3)
	v.SetDefault("kubevirt.provision_timeout_minutes", 10)
	v.SetDefault("kubevirt.max_active_sessions", 24)
	// AWS EC2 오버플로우 — 기본 비활성(max_active_sessions=0). W1 terraform 적용 후 env 로 주입해 활성화.
	v.SetDefault("aws.region", "ap-northeast-2")
	v.SetDefault("aws.launch_template_id", "") // env CLEDYU_AWS_LAUNCH_TEMPLATE_ID 로 주입
	v.SetDefault("aws.instance_type", "t3.medium")
	v.SetDefault("aws.session_ttl_hours", 3)
	v.SetDefault("aws.provision_timeout_minutes", 10)
	v.SetDefault("aws.session_key_ttl_seconds", 600)   // 동적 세션 authkey 만료(초)
	v.SetDefault("aws.session_key_tag", "tag:lab-ec2") // 동적 세션 authkey 태그
	// aws.tailscale_api_key 는 기본 빈 값 — env CLEDYU_AWS_TAILSCALE_API_KEY(Secret)로 주입. 미설정 시 정적 authkey 폴백.
	v.SetDefault("aws.max_active_sessions", 0) // 0 = EC2 오버플로우 비활성(현행 KubeVirt 전용 동작 보존)
	v.SetDefault("aws.tailnet_hostname_prefix", "lab")
	v.SetDefault("aws.tailscale_api_key", "")         // env CLEDYU_AWS_TAILSCALE_API_KEY(Secret)로 주입 — 등록해야 Unmarshal 이 바인딩
	v.SetDefault("aws.tailscale_oauth_client_id", "") // env CLEDYU_AWS_TAILSCALE_OAUTH_CLIENT_ID(선택)
	v.SetDefault("aws.tailscale_auth_key", "")        // env CLEDYU_AWS_TAILSCALE_AUTH_KEY(Secret)로 주입
	v.SetDefault("aws.api_tailscale_auth_key", "")    // env CLEDYU_AWS_API_TAILSCALE_AUTH_KEY(Secret)로 주입
	v.SetDefault("aws.api_tailnet_state_dir", "")     // 비면 tsnet 기본; k8s 는 emptyDir 경로 주입
	v.SetDefault("aws.live_terminal_ssh_user", "lab")
	v.SetDefault("aws.live_terminal_ssh_password", "lab")
	v.SetDefault("kafka.brokers", "cledyu-kafka-kafka-bootstrap.kafka.svc:9093")
	v.SetDefault("kafka.topic", "validation-requests")
	v.SetDefault("kafka.tls_cert", "/etc/kafka-certs/tls.crt")
	v.SetDefault("kafka.tls_key", "/etc/kafka-certs/tls.key")
	v.SetDefault("kafka.tls_ca", "/etc/kafka-certs/ca.crt")
	v.SetDefault("kafka.results_topic", "validation-results")
	v.SetDefault("kafka.consumer_group", "cledyu-api-validation-results")
	v.SetDefault("kafka.events_topic", "lab-events")
	v.SetDefault("ai.base_url", "") // 빈 기본값 — env CLEDYU_AI_BASE_URL 로 주입(미설정 시 정적 힌트 폴백)
	v.SetDefault("ai.timeout_seconds", 15)
	v.SetDefault("db.dsn", "") // 빈 기본값 — env CLEDYU_DB_DSN(Secret)으로 주입. 미설정 시 in-memory 전용
	// BigQuery 분석 — 빈 기본값. env CLEDYU_ANALYTICS_PROJECT_ID 설정 시 강사 분석 활성.
	v.SetDefault("analytics.project_id", "") // env CLEDYU_ANALYTICS_PROJECT_ID 로 주입
	v.SetDefault("analytics.dataset", "cledyu_analytics")
	v.SetDefault("otel.endpoint", "alloy.loki.svc.cluster.local:4317") // env CLEDYU_OTEL_ENDPOINT 로 오버라이드

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := loadMountedFileListPublicKey(&cfg); err != nil {
		return nil, err
	}
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadMountedFileListPublicKey(cfg *Config) error {
	path := strings.TrimSpace(cfg.KubeVirt.FileListSSHPublicKeyPath)
	if path == "" {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read kubevirt file-list SSH public key file: %w", err)
	}
	if key := strings.TrimSpace(string(content)); key != "" {
		cfg.KubeVirt.FileListSSHPublicKey = key
	}
	return nil
}

func validate(cfg *Config) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	labKey := strings.TrimSpace(cfg.KubeVirt.LabSSHPublicKey)
	fileListKey := strings.TrimSpace(cfg.KubeVirt.FileListSSHPublicKey)
	labMaterial, _, err := sshPublicKeyMaterial(labKey)
	if err != nil {
		return fmt.Errorf("kubevirt lab SSH public key: %w", err)
	}
	fileListMaterial, _, err := sshPublicKeyMaterial(fileListKey)
	if err != nil {
		return fmt.Errorf("kubevirt file-list SSH public key: %w", err)
	}
	if labMaterial != "" && labMaterial == fileListMaterial {
		return errors.New("kubevirt lab SSH public key and file-list SSH public key must use distinct key material")
	}
	return nil
}

func sshPublicKeyMaterial(key string) (material string, configured bool, err error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return "", false, nil
	}
	publicKey, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(trimmed))
	if err != nil {
		return "", true, err
	}
	if len(options) > 0 {
		return "", true, errors.New("must not include authorized_keys options")
	}
	if strings.TrimSpace(string(rest)) != "" {
		return "", true, errors.New("must contain exactly one SSH public key")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey))), true, nil
}
