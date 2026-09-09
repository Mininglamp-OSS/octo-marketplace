package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultHTTPWriteTimeout  = 150 * time.Second
	DefaultBotPublishTimeout = 2 * time.Minute

	// DefaultDevAuthSpaceRole is owner (model.SpaceRoleOwner) so a standalone
	// dev run can reach the reviewer-only paths without extra environment.
	DefaultDevAuthSpaceRole = 2

	// maxDevAuthSpaceRole mirrors model.SpaceRoleOwner. Duplicated as a literal
	// rather than imported so config keeps depending on nothing internal.
	maxDevAuthSpaceRole = 2

	// invalidDevAuthSpaceRole is what envSpaceRole yields for a value it cannot
	// parse. It is deliberately out of the 0..2 range so ValidateAPI rejects it
	// at boot instead of Load silently substituting the owner default.
	invalidDevAuthSpaceRole = -1

	// minOctoSecretLen is the shortest accepted octo shared secret, in bytes.
	// octo-server enforces the same floor on the mirror side at its own boot.
	minOctoSecretLen = 32

	// maxCardActionSkew is the hard ceiling on OCTO_CARD_ACTION_MAX_SKEW.
	//
	// The default is 5m, which is already generous for inter-pod NTP drift and
	// octo-server's internal retry horizon. Anything beyond ~15 minutes leaves a
	// captured valid callback replayable against a still-valid signature for
	// longer than any realistic delivery lag: the event_id receipt blocks a
	// DUPLICATE delivery, not a first delivery that was intercepted before it
	// reached us. 15 minutes is three times the default, enough to absorb a
	// badly-skewed node clock after a restart without opening a multi-hour
	// replay window by typo (e.g. 720h).
	maxCardActionSkew = 15 * time.Minute
)

type Config struct {
	AppEnv     string
	MySQLDSN   string
	OctoAPIURL string
	// OctoFleetURL is the base URL of the octo-fleet service the expert-install
	// aggregation calls out to (POST /experts/{id}/install). Empty disables the
	// install endpoint (it returns UPSTREAM_UNAVAILABLE). Mirrors OctoAPIURL.
	OctoFleetURL       string
	APIPort            string
	PublicBaseURL      string
	CORSAllowedOrigins []string
	AuthEnabled        bool
	AuthCacheTTL       time.Duration
	AuthCacheCapacity  int
	DevAuthUID         string
	DevAuthName        string
	DevSpaceID         string

	// DevAuthSpaceRole is the Space role (model.SpaceRole* encoding:
	// 0=member, 1=admin, 2=owner) granted to the fixed dev identity when
	// AUTH_ENABLED=false. It defaults to owner so a standalone developer can
	// exercise the reviewer side of the Space plugin-review flow; set it to 0
	// to exercise the submitter side, which is the whole reason this value is
	// parsed by envSpaceRole rather than envInt.
	DevAuthSpaceRole int

	// OctoInternalToken authenticates marketplace -> octo-server service calls
	// that carry no end-user token (review card dispatch). BLANK DISABLES the
	// surface: no card is sent, and the review workflow stays fully usable over
	// HTTP. That is the correct default for a deployment that has not
	// provisioned the credential — it must not be invented locally.
	OctoInternalToken string

	// OctoCardActionSecret signs/verifies the octo-server card action callback.
	// BLANK DISABLES the surface: the callback endpoint rejects everything,
	// because an unsigned callback is an unauthenticated approve button.
	OctoCardActionSecret string

	// OctoNotifyTimeout bounds an outbound notification call to octo-server.
	// Notification is best-effort and must never hold a user request open.
	OctoNotifyTimeout time.Duration

	// OctoCardActionMaxSkew is the accepted clock skew on a card action
	// callback timestamp; older or further-future callbacks are replay/stale
	// and rejected.
	OctoCardActionMaxSkew time.Duration

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ProbeAllowPrivate bool
	BotPublishTimeout time.Duration

	// Parse worker configuration for skill zip async parsing.
	SkillParseTimeout        time.Duration // single parse execution timeout
	SkillParseStaleTimeout   time.Duration // how long before parsing is considered stuck
	SkillParseMaxAttempts    int           // max recovery retries before marking failed
	SkillParseWorkerPoolSize int           // concurrent parse goroutines per pod

	// Redis URL for metrics tracking (e.g. "redis://localhost:6379/0").
	// Empty disables Redis-backed metrics (counters silently no-op).
	RedisURL string

	// Flush worker configuration for metrics persistence.
	MetricsFlushInterval time.Duration // How often to flush (default 30s)
	MetricsFlushBatch    int           // Dirty keys per SPOP (default 500)
	MetricsFlushLockTTL  time.Duration // Distributed lock TTL (default 120s)

	// Object storage for MCP icons (S3-compatible). Independent of the skill
	// archive storage below.
	Storage StorageConfig

	// Object storage (OSS/S3) configuration for skill file uploads.
	StorageDriver      string // "local" or "oss"
	LocalStorageDir    string
	OSSEndpoint        string
	OSSBucket          string
	OSSAccessKey       string
	OSSSecretKey       string
	OSSRegion          string
	OSSKeyPrefix       string
	OSSPathStyle       bool
	OSSPublicEndpoint  string
	OSSPublicPathStyle bool
	OSSSigningHost     string
	OSSDownloadSigned  bool
	MaxUploadMB        int

	Log LogConfig
}

type LogConfig struct {
	Level       string
	Format      string
	AddCaller   bool
	FileEnabled bool
	Dir         string
	MaxSizeMB   int
	MaxBackups  int
	MaxAgeDays  int
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
		AppEnv:             strings.ToLower(env("APP_ENV", "")),
		MySQLDSN:           env("MYSQL_DSN", ""),
		OctoAPIURL:         strings.TrimRight(env("OCTO_API_URL", ""), "/"),
		OctoFleetURL:       strings.TrimRight(env("OCTO_FLEET_URL", ""), "/"),
		APIPort:            env("API_PORT", "8092"),
		PublicBaseURL:      strings.TrimRight(env("PUBLIC_BASE_URL", ""), "/"),
		CORSAllowedOrigins: envCSV("CORS_ALLOWED_ORIGINS"),
		AuthEnabled:        envBool("AUTH_ENABLED", true),
		AuthCacheTTL:       envDuration("AUTH_CACHE_TTL", 30*time.Second),
		AuthCacheCapacity:  envInt("AUTH_CACHE_CAPACITY", 10000),
		DevAuthUID:         env("DEV_AUTH_UID", "dev-user"),
		DevAuthName:        env("DEV_AUTH_NAME", "Developer"),
		DevSpaceID:         env("DEV_SPACE_ID", "dev-space"),
		DevAuthSpaceRole:   envSpaceRole("DEV_AUTH_SPACE_ROLE", DefaultDevAuthSpaceRole),

		OctoInternalToken:     env("OCTO_MARKETPLACE_INTERNAL_TOKEN", ""),
		OctoCardActionSecret:  env("OCTO_MARKETPLACE_CARD_ACTION_SECRET", ""),
		OctoNotifyTimeout:     envDuration("OCTO_NOTIFY_TIMEOUT", 3*time.Second),
		OctoCardActionMaxSkew: envDuration("OCTO_CARD_ACTION_MAX_SKEW", 5*time.Minute),

		ReadHeaderTimeout: envDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       envDuration("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:      envDuration("HTTP_WRITE_TIMEOUT", DefaultHTTPWriteTimeout),
		IdleTimeout:       envDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ProbeAllowPrivate: envBool("PROBE_ALLOW_PRIVATE", false),
		BotPublishTimeout: envDuration("BOT_PUBLISH_TIMEOUT", DefaultBotPublishTimeout),

		SkillParseTimeout:        envDuration("SKILL_PARSE_TIMEOUT", 1*time.Minute),
		SkillParseStaleTimeout:   envDuration("SKILL_PARSE_STALE_TIMEOUT", 5*time.Minute),
		SkillParseMaxAttempts:    envInt("SKILL_PARSE_MAX_ATTEMPTS", 2),
		SkillParseWorkerPoolSize: envInt("SKILL_PARSE_WORKER_POOL_SIZE", 10),
		RedisURL:                 env("REDIS_URL", ""),
		MetricsFlushInterval:     envDuration("METRICS_FLUSH_INTERVAL", 30*time.Second),
		MetricsFlushBatch:        envInt("METRICS_FLUSH_BATCH", 500),
		MetricsFlushLockTTL:      envDuration("METRICS_FLUSH_LOCK_TTL", 120*time.Second),

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

		StorageDriver:      env("STORAGE_DRIVER", "local"),
		LocalStorageDir:    env("LOCAL_STORAGE_DIR", "/tmp/marketplace-uploads"),
		OSSEndpoint:        env("OSS_ENDPOINT", ""),
		OSSBucket:          env("OSS_BUCKET", ""),
		OSSAccessKey:       env("OSS_ACCESS_KEY", ""),
		OSSSecretKey:       env("OSS_SECRET_KEY", ""),
		OSSRegion:          env("OSS_REGION", "us-east-1"),
		OSSKeyPrefix:       strings.Trim(env("OSS_KEY_PREFIX", ""), "/"),
		OSSPathStyle:       envBool("OSS_PATH_STYLE", true),
		OSSPublicEndpoint:  strings.TrimRight(env("OSS_PUBLIC_ENDPOINT", ""), "/"),
		OSSPublicPathStyle: envBool("OSS_PUBLIC_PATH_STYLE", false),
		OSSSigningHost:     strings.TrimSpace(env("OSS_SIGNING_HOST", "")),
		OSSDownloadSigned:  envBool("OSS_DOWNLOAD_SIGNED", false),
		MaxUploadMB:        envInt("MAX_UPLOAD_MB", 20),
		Log: LogConfig{
			Level:       env("LOG_LEVEL", "info"),
			Format:      env("LOG_FORMAT", "console"),
			AddCaller:   envBool("LOG_ADD_CALLER", false),
			FileEnabled: envBool("LOG_FILE_ENABLED", false),
			Dir:         env("LOG_DIR", "/var/log/octo-marketplace"),
			MaxSizeMB:   envInt("LOG_MAX_SIZE_MB", 20),
			MaxBackups:  envInt("LOG_MAX_BACKUPS", 3),
			MaxAgeDays:  envInt("LOG_MAX_AGE_DAYS", 7),
		},
	}
}

// IsDev reports whether this process is explicitly running in local dev mode.
func (c Config) IsDev() bool {
	return strings.EqualFold(c.AppEnv, "dev")
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
	if c.WriteTimeout > 0 && c.BotPublishTimeout > 0 && c.WriteTimeout <= c.BotPublishTimeout {
		return fmt.Errorf("HTTP_WRITE_TIMEOUT (%s) must be greater than BOT_PUBLISH_TIMEOUT (%s)", c.WriteTimeout, c.BotPublishTimeout)
	}
	if c.DevAuthSpaceRole < 0 || c.DevAuthSpaceRole > maxDevAuthSpaceRole {
		return fmt.Errorf("DEV_AUTH_SPACE_ROLE must be 0 (member), 1 (admin) or 2 (owner)")
	}
	if err := c.validateOctoSecrets(); err != nil {
		return err
	}
	if err := c.validateCardActionSkew(); err != nil {
		return err
	}
	return validatePort(c.APIPort, "API_PORT")
}

// validateCardActionSkew enforces the upper bound on OCTO_CARD_ACTION_MAX_SKEW.
// envDuration already rejects zero/negative/malformed by falling back to the
// default, so only over-sized positive values reach here. See maxCardActionSkew
// for the rationale.
func (c Config) validateCardActionSkew() error {
	if c.OctoCardActionMaxSkew > maxCardActionSkew {
		return fmt.Errorf("OCTO_CARD_ACTION_MAX_SKEW (%s) must not exceed %s", c.OctoCardActionMaxSkew, maxCardActionSkew)
	}
	return nil
}

// validateOctoSecrets checks every configured octo shared secret. A blank
// value is not an error — it disables that surface (see the Config fields) —
// but a configured one must be long enough to resist offline guessing, and no
// two surfaces may share a value: reuse means a leak of the weakest one grants
// the others.
//
// A configured CARD_ACTION_SECRET without an INTERNAL_TOKEN is also rejected:
// the callback mounts and signatures verify, but operator-role lookup returns
// "not configured", which surfaces as 503 on every real admin click and sends
// every event into the DLQ. That is a boot-time error rather than a silent
// misconfiguration, because there is no confidentiality argument for leaving
// the callback partially open the way there is for leaving it fully closed.
//
// Error messages name the variable and never echo the value.
func (c Config) validateOctoSecrets() error {
	secrets := []struct{ name, value string }{
		{"OCTO_MARKETPLACE_INTERNAL_TOKEN", c.OctoInternalToken},
		{"OCTO_MARKETPLACE_CARD_ACTION_SECRET", c.OctoCardActionSecret},
	}
	seen := make(map[string]string, len(secrets))
	for _, secret := range secrets {
		if secret.value == "" {
			continue
		}
		if len(secret.value) < minOctoSecretLen {
			return fmt.Errorf("%s must be at least %d bytes", secret.name, minOctoSecretLen)
		}
		if other, duplicate := seen[secret.value]; duplicate {
			return fmt.Errorf("%s must not reuse the value of %s", secret.name, other)
		}
		seen[secret.value] = secret.name
	}
	if c.OctoCardActionSecret != "" && c.OctoInternalToken == "" {
		return fmt.Errorf("OCTO_MARKETPLACE_CARD_ACTION_SECRET requires OCTO_MARKETPLACE_INTERNAL_TOKEN: the callback verifies signatures but cannot authorize operator roles without the internal token, so every valid admin click would 503")
	}
	if c.OctoCardActionSecret != "" && c.OctoAPIURL == "" {
		return fmt.Errorf("OCTO_MARKETPLACE_CARD_ACTION_SECRET requires OCTO_API_URL: the callback re-derives operator roles against octo-server at that URL; without it every valid admin click fails role lookup and returns 503")
	}
	return nil
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

// envSpaceRole is envInt's sibling for the Space role encoding, where 0 is a
// meaningful configured value (plain member) rather than "unset".
//
// envInt cannot be used here and must not be changed to suit this caller: its
// other callers are sizes, limits and pool sizes where <= 0 really does mean
// unset, and relaxing it would let MAX_UPLOAD_MB=0 through. Here the same rule
// would silently promote a deliberately-configured DEV_AUTH_SPACE_ROLE=0 to the
// owner default, making the non-reviewer path impossible to exercise locally —
// exactly the case a developer sets the variable to reach.
//
// Only an empty/unset variable falls back. An unparseable one yields
// invalidDevAuthSpaceRole so ValidateAPI fails the boot with a named error;
// DEV_AUTH_SPACE_ROLE=admin is a typo, and quietly running as owner is the one
// outcome that must not follow from it. (The alternative — falling back like
// every other parser here — was rejected for that reason; the cost is that a
// stray junk value now fails boot even on a deployment where AUTH_ENABLED=true
// makes the dev role unused.)
func envSpaceRole(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return invalidDevAuthSpaceRole
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
