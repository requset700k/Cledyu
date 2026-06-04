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
	FrontendURL string         `mapstructure:"frontend_url"`
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
