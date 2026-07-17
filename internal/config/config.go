package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	MySQLDSN           string
	OctoAPIURL         string
	APIPort            string
	PublicBaseURL      string
	CORSAllowedOrigins []string
	AuthEnabled        bool
	AuthCacheTTL       time.Duration
	AuthCacheCapacity  int
	DevAuthUID         string
	DevAuthName        string
	DevSpaceID         string
	ReadHeaderTimeout  time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	ProbeAllowPrivate  bool

	// Parse worker configuration for skill zip async parsing.
	SkillParseTimeout        time.Duration // single parse execution timeout
	SkillParseStaleTimeout   time.Duration // how long before parsing is considered stuck
	SkillParseMaxAttempts    int           // max recovery retries before marking failed
	SkillParseWorkerPoolSize int           // concurrent parse goroutines per pod

	// Parse worker configuration for skill zip async parsing.
	SkillParseTimeout        time.Duration // single parse execution timeout
	SkillParseStaleTimeout   time.Duration // how long before parsing is considered stuck
	SkillParseMaxAttempts    int           // max recovery retries before marking failed
	SkillParseWorkerPoolSize int           // concurrent parse goroutines per pod

	// Object storage for MCP icons (S3-compatible). Independent of the skill
	// archive storage below.
	Storage StorageConfig

	// Object storage (OSS/S3) configuration for skill file uploads.
	StorageDriver     string // "local" or "oss"
	LocalStorageDir   string
	OSSEndpoint       string
	OSSBucket         string
	OSSAccessKey      string
	OSSSecretKey      string
	OSSRegion         string
	OSSKeyPrefix      string
	OSSPathStyle      bool
	OSSPublicEndpoint string
	OSSSigningHost    string
	OSSDownloadSigned bool
	MaxUploadMB       int
}

// StorageConfig configures the S3-compatible object store used for MCP icons.
type StorageConfig struct {
	Endpoint      string
	Region        string
	Bucket        string
	AccessKey     string
	SecretKey     string
	PublicBaseURL string
	IconPartition string
	PathStyle     bool
}

// Enabled reports whether object storage is configured well enough to accept
// icon uploads. A missing bucket disables the feature rather than failing
// startup, keeping local dev runnable without storage.
func (s StorageConfig) Enabled() bool {
	return s.Bucket != "" && s.Endpoint != "" && s.AccessKey != "" && s.SecretKey != ""
}

func Load() Config {
	return Config{
		MySQLDSN:           env("MYSQL_DSN", ""),
		OctoAPIURL:         strings.TrimRight(env("OCTO_API_URL", ""), "/"),
		APIPort:            env("API_PORT", "8092"),
		PublicBaseURL:      strings.TrimRight(env("PUBLIC_BASE_URL", ""), "/"),
		CORSAllowedOrigins: envCSV("CORS_ALLOWED_ORIGINS"),
		AuthEnabled:        envBool("AUTH_ENABLED", true),
		AuthCacheTTL:       envDuration("AUTH_CACHE_TTL", 30*time.Second),
		AuthCacheCapacity:  envInt("AUTH_CACHE_CAPACITY", 10000),
		DevAuthUID:         env("DEV_AUTH_UID", "dev-user"),
		DevAuthName:        env("DEV_AUTH_NAME", "Developer"),
		DevSpaceID:         env("DEV_SPACE_ID", "dev-space"),
		ReadHeaderTimeout:  envDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:        envDuration("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:       envDuration("HTTP_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:        envDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ProbeAllowPrivate:  envBool("PROBE_ALLOW_PRIVATE", false),

		SkillParseTimeout:        envDuration("SKILL_PARSE_TIMEOUT", 1*time.Minute),
		SkillParseStaleTimeout:   envDuration("SKILL_PARSE_STALE_TIMEOUT", 5*time.Minute),
		SkillParseMaxAttempts:    envInt("SKILL_PARSE_MAX_ATTEMPTS", 2),
		SkillParseWorkerPoolSize: envInt("SKILL_PARSE_WORKER_POOL_SIZE", 10),

		Storage: StorageConfig{
			Endpoint:      strings.TrimRight(env("STORAGE_ENDPOINT", ""), "/"),
			Region:        env("STORAGE_REGION", "us-east-1"),
			Bucket:        env("STORAGE_BUCKET", ""),
			AccessKey:     env("STORAGE_ACCESS_KEY", ""),
			SecretKey:     env("STORAGE_SECRET_KEY", ""),
			PublicBaseURL: strings.TrimRight(env("STORAGE_PUBLIC_BASE_URL", ""), "/"),
			IconPartition: env("STORAGE_ICON_PARTITION", "mcp"),
			PathStyle:     envBool("STORAGE_PATH_STYLE", true),
		},

		StorageDriver:     env("STORAGE_DRIVER", "local"),
		LocalStorageDir:   env("LOCAL_STORAGE_DIR", "/tmp/marketplace-uploads"),
		OSSEndpoint:       env("OSS_ENDPOINT", ""),
		OSSBucket:         env("OSS_BUCKET", ""),
		OSSAccessKey:      env("OSS_ACCESS_KEY", ""),
		OSSSecretKey:      env("OSS_SECRET_KEY", ""),
		OSSRegion:         env("OSS_REGION", "us-east-1"),
		OSSKeyPrefix:      strings.Trim(env("OSS_KEY_PREFIX", ""), "/"),
		OSSPathStyle:      envBool("OSS_PATH_STYLE", true),
		OSSPublicEndpoint: strings.TrimRight(env("OSS_PUBLIC_ENDPOINT", ""), "/"),
		OSSSigningHost:    strings.TrimSpace(env("OSS_SIGNING_HOST", "")),
		OSSDownloadSigned: envBool("OSS_DOWNLOAD_SIGNED", false),
		MaxUploadMB:       envInt("MAX_UPLOAD_MB", 20),
	}
}

func (c Config) ValidateAPI() error {
	if c.MySQLDSN == "" {
		return fmt.Errorf("MYSQL_DSN is required")
	}
	if c.AuthEnabled && c.OctoAPIURL == "" {
		return fmt.Errorf("OCTO_API_URL is required when AUTH_ENABLED=true")
	}
	// Parse worker config: staleTimeout must be strictly greater than parseTimeout
	// so a legitimately-running parse task is not prematurely reclaimed.
	if c.SkillParseStaleTimeout <= c.SkillParseTimeout {
		return fmt.Errorf("SKILL_PARSE_STALE_TIMEOUT (%s) must be greater than SKILL_PARSE_TIMEOUT (%s)", c.SkillParseStaleTimeout, c.SkillParseTimeout)
	}
	// Parse worker config: staleTimeout must be strictly greater than parseTimeout
	// so a legitimately-running parse task is not prematurely reclaimed.
	if c.SkillParseStaleTimeout <= c.SkillParseTimeout {
		return fmt.Errorf("SKILL_PARSE_STALE_TIMEOUT (%s) must be greater than SKILL_PARSE_TIMEOUT (%s)", c.SkillParseStaleTimeout, c.SkillParseTimeout)
	}
	return validatePort(c.APIPort, "API_PORT")
}

func validatePort(value, name string) error {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%s must be a valid TCP port", name)
	}
	return nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envCSV(key string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
