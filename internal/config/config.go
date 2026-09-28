// Package config loads runtime configuration from environment variables.
package config

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"rentmapgh/internal/platform/sms"
	"rentmapgh/internal/platform/storage"
)

// devAuthSecret is only accepted in development; Load refuses it elsewhere.
const devAuthSecret = "dev-only-insecure-auth-secret-change-me" //nolint:gosec // G101: the public dev default, rejected outside development

type Env string

const (
	EnvDevelopment Env = "development"
	EnvStaging     Env = "staging"
	EnvProduction  Env = "production"
)

type Config struct {
	Env      Env    `env:"APP_ENV" envDefault:"development"`
	Addr     string `env:"HTTP_ADDR" envDefault:":8080"`
	BaseURL  string `env:"BASE_URL" envDefault:"http://localhost:8080"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`

	DatabaseURL    string `env:"DATABASE_URL,required,notEmpty"`
	DBMaxConns     int32  `env:"DB_MAX_CONNS" envDefault:"10"`
	MigrateOnBoot  bool   `env:"MIGRATE_ON_BOOT" envDefault:"false"`
	StaticFromDisk bool   `env:"STATIC_FROM_DISK" envDefault:"false"` // dev: serve web/static live instead of the embedded copy
	// TrustProxy honours X-Forwarded-For / CF-Connecting-IP. Only enable behind
	// Caddy/Cloudflare, otherwise clients can spoof their IP past rate limits.
	TrustProxy bool `env:"TRUST_PROXY" envDefault:"false"`

	// AuthSecret keys the OTP-code HMAC and signed auth cookies (≥32 chars).
	AuthSecret string        `env:"AUTH_SECRET" envDefault:"dev-only-insecure-auth-secret-change-me"`
	SessionTTL time.Duration `env:"SESSION_TTL" envDefault:"720h"` // sliding: extended while the device is in use
	// SMSDailyCap is a global circuit breaker on OTP sends (SMS-pumping / toll fraud).
	SMSDailyCap int `env:"SMS_DAILY_CAP" envDefault:"2000"`
	SMS         sms.Config

	// StorageDir holds private uploads (verification evidence, avatars).
	// In production it's a Docker volume, never under web/static.
	StorageDir string `env:"STORAGE_DIR" envDefault:"./data/private"`
	// DocumentKey is base64 of 32 random bytes; it encrypts verification
	// evidence at rest. Losing it makes stored evidence unreadable.
	DocumentKey string `env:"DOCUMENT_KEY"`
	// LocationSecret seeds the fixed 150–400 m offset of every public map
	// point (ProjectRequirement §6.1). NEVER rotate it: two different offsets
	// for the same property let anyone narrow down the real location.
	LocationSecret string `env:"LOCATION_SECRET" envDefault:"dev-only-location-secret"`
	// MediaStore holds public listing photos: "disk" (MEDIA_DIR, a volume) or
	// "s3" (SeaweedFS in dev, R2/Garage in production).
	MediaStore string `env:"MEDIA_STORE" envDefault:"disk"`
	MediaDir   string `env:"MEDIA_DIR" envDefault:"./data/media"`
	S3         storage.S3Config
	// Walk-through videos need ffmpeg + ffprobe (on PATH unless set). Uploads
	// are assembled and transcoded in VideoInbox, a local volume; the
	// original files never leave it.
	VideoEnabled bool   `env:"VIDEO_ENABLED" envDefault:"true"`
	FFmpegPath   string `env:"FFMPEG_PATH"`
	FFprobePath  string `env:"FFPROBE_PATH"`
	VideoInbox   string `env:"VIDEO_INBOX_DIR" envDefault:"./data/video-inbox"`
	// EvidenceRetention: ID photos are deleted this long after a decision.
	EvidenceRetention time.Duration `env:"EVIDENCE_RETENTION" envDefault:"2160h"`

	// SupportEmail is shown on the legal and help pages ("" shows a
	// placeholder until the support inbox exists).
	SupportEmail string `env:"SUPPORT_EMAIL"`
	// SentryDSN turns on error reporting (panics and ERROR logs); "" = off.
	SentryDSN string `env:"SENTRY_DSN"`

	ReadTimeout     time.Duration `env:"HTTP_READ_TIMEOUT" envDefault:"10s"`
	WriteTimeout    time.Duration `env:"HTTP_WRITE_TIMEOUT" envDefault:"30s"`
	IdleTimeout     time.Duration `env:"HTTP_IDLE_TIMEOUT" envDefault:"120s"`
	ShutdownTimeout time.Duration `env:"HTTP_SHUTDOWN_TIMEOUT" envDefault:"20s"`
}

func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

func (c Config) IsDev() bool { return c.Env == EnvDevelopment }

// IsHTTPS reports whether the public site is served over TLS (TLS itself is
// terminated by Caddy, so the Go server can't see it on the request).
func (c Config) IsHTTPS() bool { return strings.HasPrefix(c.BaseURL, "https://") }

func (c Config) validate() error {
	switch c.Env {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		return fmt.Errorf("APP_ENV must be development, staging or production, got %q", c.Env)
	}
	if c.Env == EnvProduction && c.StaticFromDisk {
		return errors.New("STATIC_FROM_DISK must be false in production")
	}
	if c.Env != EnvDevelopment && !c.IsHTTPS() {
		return errors.New("BASE_URL must be https outside development")
	}
	if c.Env != EnvDevelopment && (c.AuthSecret == devAuthSecret || len(c.AuthSecret) < 32) {
		return errors.New("AUTH_SECRET must be set to a random string of at least 32 characters outside development")
	}
	if c.SessionTTL < time.Hour {
		return errors.New("SESSION_TTL must be at least 1h")
	}
	if err := c.SMS.Validate(c.Env == EnvProduction); err != nil {
		return err
	}
	if c.Env != EnvDevelopment && (c.LocationSecret == "dev-only-location-secret" || len(c.LocationSecret) < 32) {
		return errors.New("LOCATION_SECRET must be a random string of at least 32 characters outside development (and never rotated)")
	}
	if c.Env != EnvDevelopment && c.DocumentKey == "" {
		return errors.New("DOCUMENT_KEY is required outside development (openssl rand -base64 32)")
	}
	switch c.MediaStore {
	case "disk":
	case "s3":
		if err := c.S3.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("MEDIA_STORE must be disk or s3, got %q", c.MediaStore)
	}
	if _, err := c.DocumentKeyBytes(); err != nil {
		return err
	}
	return nil
}

// DocumentKeyBytes decodes DOCUMENT_KEY. In development an unset key falls
// back to one derived from the (public) dev auth secret.
func (c Config) DocumentKeyBytes() ([]byte, error) {
	if c.DocumentKey == "" {
		sum := sha256.Sum256([]byte("rentmap-dev-document-key:" + c.AuthSecret))
		return sum[:], nil
	}
	k, err := base64.StdEncoding.DecodeString(c.DocumentKey)
	if err != nil || len(k) != 32 {
		return nil, errors.New("DOCUMENT_KEY must be base64 of exactly 32 bytes (openssl rand -base64 32)")
	}
	return k, nil
}
