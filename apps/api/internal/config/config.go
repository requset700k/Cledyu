// 우선순위: 환경변수 (CLEDYU_*) > config.yaml > 기본값.
package config

import (
	"errors"
	"fmt"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Redis    RedisConfig    `mapstructure:"redis"`
	Keycloak KeycloakConfig `mapstructure:"keycloak"`
}

type KeycloakConfig struct {
	URL          string `mapstructure:"url"`
	Realm        string `mapstructure:"realm"`
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	RedirectURI  string `mapstructure:"redirect_uri"`
	FrontendURL  string `mapstructure:"frontend_url"`
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
	v.AutomaticEnv()
	// viper가 중첩 키(keycloak.frontend_url 등)를 env로 못 읽는 문제 방지.
	v.BindEnv("keycloak.frontend_url", "CLEDYU_FRONTEND_URL")            //nolint:errcheck
	v.BindEnv("keycloak.client_secret", "CLEDYU_KEYCLOAK_CLIENT_SECRET") //nolint:errcheck
	v.BindEnv("keycloak.cookie_domain", "CLEDYU_KEYCLOAK_COOKIE_DOMAIN") //nolint:errcheck

	v.SetDefault("server.addr", ":8080")
	v.SetDefault("server.mode", "debug")
	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("keycloak.url", "https://keycloak.cledyu.local")
	v.SetDefault("keycloak.realm", "cledyu")
	v.SetDefault("keycloak.client_id", "cledyu-web")
	v.SetDefault("keycloak.redirect_uri", "https://api.cledyu.local/api/v1/auth/callback")
	v.SetDefault("keycloak.frontend_url", "https://app.cledyu.local")
	v.SetDefault("keycloak.cookie_domain", ".cledyu.local")

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
