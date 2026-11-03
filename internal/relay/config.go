package relay

import (
	"os"
	"time"
)

type Config struct {
	HTTPAddr, DatabaseDSN, JWTSecret, JWTIssuer, JWTAudience, AdminToken, EncryptionKey string
	UpstreamTimeout                                                                     time.Duration
}

func LoadConfig() Config {
	timeout, err := time.ParseDuration(env("UPSTREAM_TIMEOUT", "90s"))
	if err != nil || timeout < time.Second {
		timeout = 90 * time.Second
	}
	return Config{HTTPAddr: env("HTTP_ADDR", ":8088"), DatabaseDSN: os.Getenv("DATABASE_DSN"),
		JWTSecret: os.Getenv("JWT_HS256_SECRET"), JWTIssuer: env("JWT_ISSUER", "cx-member"),
		JWTAudience: env("JWT_AUDIENCE", "cx-ai-relay"), AdminToken: os.Getenv("ADMIN_BEARER_TOKEN"), EncryptionKey: os.Getenv("ENCRYPTION_KEY"), UpstreamTimeout: timeout}
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
