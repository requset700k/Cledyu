// 우선순위: 환경변수 (CLEDYU_*) > config.yaml > 기본값.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server      ServerConfig   `mapstructure:"server"`
	Redis       RedisConfig    `mapstructure:"redis"`
	Keycloak    KeycloakConfig `mapstructure:"keycloak"`
	KubeVirt    KubeVirtConfig `mapstructure:"kubevirt"`
	Kafka       KafkaConfig    `mapstructure:"kafka"`
	AI          AIConfig       `mapstructure:"ai"`
	DB          DBConfig       `mapstructure:"db"`
	FrontendURL string         `mapstructure:"frontend_url"`
}

// DBConfig는 PostgreSQL 영속 계층 설정이다.
// DSN 이 비면 영속화 비활성 — 진행 상태는 in-memory 전용으로 동작한다(로컬/CI).
// DSN 에 비밀번호가 포함되므로 값 전체를 Secret(ESO: cledyu-api-db)으로 주입한다.
type DBConfig struct {
	DSN string `mapstructure:"dsn"` // 예: postgres://cledyu:***@postgres.postgres.svc:5432/cledyu
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
	// ProvisionTimeoutMinutes: 세션 VM이 이 시간 내 ready(Running)가 안 되면 Get은 failed로 표시하고 reaper가 세션을 회수(삭제)한다.
	// stuck provisioning이 CDI 클론 재시도 thrash로 번지는 것을 차단. 0이면 비활성.
	ProvisionTimeoutMinutes int `mapstructure:"provision_timeout_minutes"`
	// MaxActiveSessions: 클러스터 전체 동시 활성 세션 상한. 초과 시 세션 생성을 429로 거부한다.
	// 스토리지/컴퓨트 용량을 넘어서는 무한정 세션 생성으로 Longhorn이 마르는 것을 방지. 0이면 무제한.
	MaxActiveSessions int `mapstructure:"max_active_sessions"`
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
	v.SetDefault("kubevirt.kubeconfig", os.Getenv("KUBECONFIG"))
	v.SetDefault("kubevirt.base_image_ns", "kubevirt")
	v.SetDefault("kubevirt.base_image_name", "ubuntu-2204-base")
	v.SetDefault("kubevirt.lab_ssh_public_key", "") // 빈 기본값 — env CLEDYU_KUBEVIRT_LAB_SSH_PUBLIC_KEY로 주입
	v.SetDefault("kubevirt.storage_class", "longhorn-r2")
	v.SetDefault("kubevirt.session_ttl_hours", 3)
	v.SetDefault("kubevirt.provision_timeout_minutes", 2)
	v.SetDefault("kubevirt.max_active_sessions", 24)
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
	return &cfg, nil
}
