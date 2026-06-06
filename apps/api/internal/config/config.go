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

// KafkaConfig는 검증 요청 발행용 Kafka 연결 설정이다.
// Enabled=false(기본)면 발행을 건너뛴다 — 클러스터/인증서가 준비되기 전까지 안전하게 비활성.
type KafkaConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Brokers string `mapstructure:"brokers"` // 콤마 구분 mTLS 리스너 주소(:9093)
	Topic   string `mapstructure:"topic"`   // 검증 요청 발행 토픽(validation-requests)
	TLSCert string `mapstructure:"tls_cert"`
	TLSKey  string `mapstructure:"tls_key"`
	CACert  string `mapstructure:"ca_cert"`

	// 검증 결과 소비(consumer) 설정. ResultsTopic 을 구독해 stepStore 를 실제 결과로 갱신한다.
	ResultsTopic  string `mapstructure:"results_topic"`
	ConsumerGroup string `mapstructure:"consumer_group"`
}

type KubeVirtConfig struct {
	Kubeconfig      string `mapstructure:"kubeconfig"`
	BaseImageNS     string `mapstructure:"base_image_ns"`
	BaseImageName   string `mapstructure:"base_image_name"`
	SessionTTLHours int    `mapstructure:"session_ttl_hours"`
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
	v.SetDefault("keycloak.realm", "cledyu")
	v.SetDefault("keycloak.client_id", "cledyu-web")
	v.SetDefault("keycloak.redirect_uri", "https://api.cledyu.local/api/v1/auth/callback")
	v.SetDefault("frontend_url", "https://app.cledyu.local")
	v.SetDefault("keycloak.cookie_domain", ".cledyu.local")
	v.SetDefault("kubevirt.base_image_ns", "kubevirt")
	v.SetDefault("kubevirt.base_image_name", "ubuntu-2204-base")
	v.SetDefault("kubevirt.session_ttl_hours", 3)
	v.SetDefault("kafka.enabled", false)
	v.SetDefault("kafka.brokers", "cledyu-kafka-kafka-bootstrap.kafka.svc:9093")
	v.SetDefault("kafka.topic", "validation-requests")
	v.SetDefault("kafka.tls_cert", "/etc/kafka-certs/tls.crt")
	v.SetDefault("kafka.tls_key", "/etc/kafka-certs/tls.key")
	v.SetDefault("kafka.ca_cert", "/etc/kafka-certs/ca.crt")
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
