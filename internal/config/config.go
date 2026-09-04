package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL           string
	LogDir                string
	ListenAddr            string
	PublicURL             string
	MasterKey             []byte
	CookieSecure          bool
	AutoMigrate           bool
	Workers               int
	AllowPrivateUpstreams bool
	SessionTTL            time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:           strings.TrimSpace(os.Getenv("S2AM_DATABASE_URL")),
		LogDir:                env("S2AM_LOG_DIR", "./logs"),
		ListenAddr:            env("S2AM_LISTEN_ADDR", ":33777"),
		PublicURL:             strings.TrimRight(env("S2AM_PUBLIC_URL", "http://127.0.0.1:33777"), "/"),
		CookieSecure:          envBool("S2AM_COOKIE_SECURE", false),
		AutoMigrate:           envBool("S2AM_AUTO_MIGRATE", true),
		Workers:               envInt("S2AM_WORKERS", 8),
		AllowPrivateUpstreams: envBool("S2AM_ALLOW_PRIVATE_UPSTREAMS", false),
		SessionTTL:            30 * 24 * time.Hour,
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("S2AM_DATABASE_URL is required")
	}
	if cfg.Workers < 1 || cfg.Workers > 128 {
		return Config{}, errors.New("S2AM_WORKERS must be between 1 and 128")
	}
	if _, err := url.ParseRequestURI(cfg.PublicURL); err != nil {
		return Config{}, fmt.Errorf("invalid S2AM_PUBLIC_URL: %w", err)
	}
	key, err := parseMasterKey(os.Getenv("S2AM_MASTER_KEY"))
	if err != nil {
		return Config{}, err
	}
	cfg.MasterKey = key
	return cfg, nil
}

func parseMasterKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("S2AM_MASTER_KEY is required; generate one with: openssl rand -base64 32")
	}
	for _, decode := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, hex.DecodeString} {
		if value, err := decode(raw); err == nil && len(value) == 32 {
			return value, nil
		}
	}
	// A literal passphrase is deliberately rejected instead of silently using a weak key.
	return nil, errors.New("S2AM_MASTER_KEY must encode exactly 32 bytes as base64 or hex")
}

func KeyFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
