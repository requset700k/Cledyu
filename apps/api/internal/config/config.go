// 우선순위: 환경변수 (CLEDYU_*) > config.yaml > 기본값.
package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server      ServerConfig   `mapstructure:"server"`
	Redis       RedisConfig    `mapstructure:"redis"`
	Keycloak    KeycloakConfig `mapstructure:"keycloak"`
	KubeVirt    KubeVirtConfig `mapstructure:"kubevirt"`
	Kafka       KafkaConfig    `mapstructure:"kafka"`
	FrontendURL string         `mapstructure:"frontend_url"`
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
}

type KeycloakConfig struct {
	URL          string `mapstructure:"url"`
	Realm        string `mapstructure:"realm"`
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	RedirectURI  string `mapstructure:"redirect_uri"`
	CookieDomain string `mapstructure:"cookie_domain"`
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
	v.SetDefault("keycloak.redirect_uri", "https://api.cledyu.local/api/v1/auth/callback")
	v.SetDefault("frontend_url", "https://app.cledyu.local")
	v.SetDefault("keycloak.cookie_domain", ".cledyu.local")
	v.SetDefault("kubevirt.base_image_ns", "kubevirt")
	v.SetDefault("kubevirt.base_image_name", "ubuntu-2204-base")
	v.SetDefault("kubevirt.lab_ssh_public_key", "") // 빈 기본값 — env CLEDYU_KUBEVIRT_LAB_SSH_PUBLIC_KEY로 주입
	v.SetDefault("kubevirt.storage_class", "longhorn-r2")
	v.SetDefault("kubevirt.session_ttl_hours", 3)
	v.SetDefault("kafka.brokers", "cledyu-kafka-kafka-bootstrap.kafka.svc:9093")
	v.SetDefault("kafka.topic", "validation-requests")
	v.SetDefault("kafka.tls_cert", "/etc/kafka-certs/tls.crt")
	v.SetDefault("kafka.tls_key", "/etc/kafka-certs/tls.key")
	v.SetDefault("kafka.tls_ca", "/etc/kafka-certs/ca.crt")
	v.SetDefault("kafka.results_topic", "validation-results")
	v.SetDefault("kafka.consumer_group", "cledyu-api-validation-results")

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
