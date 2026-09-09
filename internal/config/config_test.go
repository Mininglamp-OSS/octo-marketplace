package config

import (
	"strings"
	"testing"
	"time"
)

// buildValid is the baseline for validation tests: every required field at a
// valid value, so a single mutation is what the test case asserts against.
func buildValid() Config {
	return Config{
		MySQLDSN: "dsn", APIPort: "8092",
		SkillParseTimeout: time.Minute, SkillParseStaleTimeout: 5 * time.Minute,
		// Default max skew (5m) is within the 15m ceiling; explicit so tests
		// that don't mutate it don't accidentally depend on the const.
		OctoCardActionMaxSkew: 5 * time.Minute,
	}
}

// Deterministic hex fixtures — not real credentials.
const (
	tokA = "0123456789abcdef0123456789abcdef" // gitleaks:allow
	tokB = "fedcba9876543210fedcba9876543210" // gitleaks:allow
	// tokShort is below the 32-byte minimum and must be rejected with a named
	// error, never silently accepted.
	tokShort = "0123456789abcdef"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("MYSQL_DSN", "test-dsn")
	t.Setenv("API_PORT", "")
	t.Setenv("HTTP_READ_HEADER_TIMEOUT", "")
	cfg := Load()
	if cfg.APIPort != "8092" {
		t.Fatalf("APIPort=%q want=8092", cfg.APIPort)
	}
	if cfg.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout=%v want=5s", cfg.ReadHeaderTimeout)
	}
	if cfg.WriteTimeout != DefaultHTTPWriteTimeout {
		t.Fatalf("WriteTimeout=%v want=%v", cfg.WriteTimeout, DefaultHTTPWriteTimeout)
	}
	if cfg.BotPublishTimeout != DefaultBotPublishTimeout {
		t.Fatalf("BotPublishTimeout=%v want=%v", cfg.BotPublishTimeout, DefaultBotPublishTimeout)
	}
	if cfg.WriteTimeout <= cfg.BotPublishTimeout {
		t.Fatalf("WriteTimeout=%v must be greater than BotPublishTimeout=%v", cfg.WriteTimeout, cfg.BotPublishTimeout)
	}
	if cfg.Log.Level != "info" {
		t.Fatalf("Log.Level=%q want=info", cfg.Log.Level)
	}
	if cfg.Log.Format != "console" {
		t.Fatalf("Log.Format=%q want=console", cfg.Log.Format)
	}
	if cfg.Log.AddCaller {
		t.Fatal("Log.AddCaller=true want=false")
	}
	if cfg.Log.FileEnabled {
		t.Fatal("Log.FileEnabled=true want=false")
	}
	if cfg.Log.Dir != "/var/log/octo-marketplace" {
		t.Fatalf("Log.Dir=%q want=/var/log/octo-marketplace", cfg.Log.Dir)
	}
	if cfg.Log.MaxSizeMB != 20 {
		t.Fatalf("Log.MaxSizeMB=%d want=20", cfg.Log.MaxSizeMB)
	}
	if cfg.Log.MaxBackups != 3 {
		t.Fatalf("Log.MaxBackups=%d want=3", cfg.Log.MaxBackups)
	}
	if cfg.Log.MaxAgeDays != 7 {
		t.Fatalf("Log.MaxAgeDays=%d want=7", cfg.Log.MaxAgeDays)
	}
}

func TestPublicBaseURLTrimsTrailingSlash(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://api.example.com/marketplace/")
	if got := Load().PublicBaseURL; got != "https://api.example.com/marketplace" {
		t.Fatalf("PublicBaseURL=%q", got)
	}
}

func TestCORSAllowedOriginsFromEnv(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", " https://octo.example.com , ,https://admin.octo.example.com/ ")
	got := Load().CORSAllowedOrigins
	want := []string{"https://octo.example.com", "https://admin.octo.example.com/"}
	if len(got) != len(want) {
		t.Fatalf("CORSAllowedOrigins=%q want=%q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CORSAllowedOrigins[%d]=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestValidateAPI(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "valid", cfg: buildValid()},
		{name: "missing dsn", cfg: func() Config {
			c := buildValid()
			c.MySQLDSN = ""
			return c
		}(), wantErr: true},
		{name: "invalid port", cfg: func() Config {
			c := buildValid()
			c.APIPort = "0"
			return c
		}(), wantErr: true},
		{name: "staleTimeout <= parseTimeout", cfg: func() Config {
			c := buildValid()
			c.SkillParseTimeout = 5 * time.Minute
			c.SkillParseStaleTimeout = 5 * time.Minute
			return c
		}(), wantErr: true},
		{name: "writeTimeout <= botPublishTimeout", cfg: func() Config {
			c := buildValid()
			c.WriteTimeout = 30 * time.Second
			c.BotPublishTimeout = 2 * time.Minute
			return c
		}(), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ValidateAPI(); (got != nil) != tt.wantErr {
				t.Fatalf("ValidateAPI() error=%v wantErr=%v", got, tt.wantErr)
			}
		})
	}
}

func TestInvalidDurationFallsBack(t *testing.T) {
	t.Setenv("MYSQL_DSN", "test-dsn")
	t.Setenv("HTTP_READ_TIMEOUT", "invalid")
	if got := Load().ReadTimeout; got != 15*time.Second {
		t.Fatalf("ReadTimeout=%v want=15s", got)
	}
}

func TestAuthEnabledByDefault(t *testing.T) {
	t.Setenv("AUTH_ENABLED", "")
	if !Load().AuthEnabled {
		t.Fatal("AuthEnabled=false want=true")
	}
}

func TestProbeAllowPrivateFromEnv(t *testing.T) {
	t.Setenv("PROBE_ALLOW_PRIVATE", "true")
	if !Load().ProbeAllowPrivate {
		t.Fatal("ProbeAllowPrivate=false want=true")
	}
}

func TestAuthEnabledRequiresOctoAPIURL(t *testing.T) {
	cfg := buildValid()
	cfg.AuthEnabled = true
	cfg.OctoAPIURL = ""
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("ValidateAPI() error=nil want OCTO_API_URL error")
	}
}

func TestOSSConfigDefaults(t *testing.T) {
	t.Setenv("MYSQL_DSN", "test-dsn")
	t.Setenv("OSS_ENDPOINT", "")
	t.Setenv("OSS_BUCKET", "")
	t.Setenv("OSS_ACCESS_KEY", "")
	t.Setenv("OSS_SECRET_KEY", "")
	t.Setenv("OSS_KEY_PREFIX", "")
	t.Setenv("OSS_PATH_STYLE", "")
	t.Setenv("OSS_PUBLIC_ENDPOINT", "")
	t.Setenv("OSS_PUBLIC_PATH_STYLE", "")
	t.Setenv("OSS_SIGNING_HOST", "")
	t.Setenv("MAX_UPLOAD_MB", "")

	cfg := Load()
	if cfg.OSSEndpoint != "" {
		t.Fatalf("OSSEndpoint=%q want empty", cfg.OSSEndpoint)
	}
	if cfg.OSSBucket != "" {
		t.Fatalf("OSSBucket=%q want empty", cfg.OSSBucket)
	}
	if cfg.MaxUploadMB != 20 {
		t.Fatalf("MaxUploadMB=%d want=20", cfg.MaxUploadMB)
	}
	if cfg.OSSPublicPathStyle {
		t.Fatal("OSSPublicPathStyle=true want default false")
	}
}

func TestOSSConfigFromEnv(t *testing.T) {
	t.Setenv("MYSQL_DSN", "test-dsn")
	t.Setenv("OSS_ENDPOINT", "https://oss.example.com")
	t.Setenv("OSS_BUCKET", "my-bucket")
	t.Setenv("OSS_ACCESS_KEY", "ak")
	t.Setenv("OSS_SECRET_KEY", "sk")
	t.Setenv("OSS_REGION", "ap-beijing")
	t.Setenv("OSS_KEY_PREFIX", "/im-test/marketplace/")
	t.Setenv("OSS_PATH_STYLE", "false")
	t.Setenv("OSS_PUBLIC_ENDPOINT", "https://cdn.example.com/")
	t.Setenv("OSS_PUBLIC_PATH_STYLE", "true")
	t.Setenv("OSS_SIGNING_HOST", "my-bucket.cos.ap-beijing.myqcloud.com")
	t.Setenv("OSS_DOWNLOAD_SIGNED", "true")
	t.Setenv("MAX_UPLOAD_MB", "50")

	cfg := Load()
	if cfg.OSSEndpoint != "https://oss.example.com" {
		t.Fatalf("OSSEndpoint=%q", cfg.OSSEndpoint)
	}
	if cfg.OSSBucket != "my-bucket" {
		t.Fatalf("OSSBucket=%q", cfg.OSSBucket)
	}
	if cfg.OSSAccessKey != "ak" {
		t.Fatalf("OSSAccessKey=%q", cfg.OSSAccessKey)
	}
	if cfg.OSSSecretKey != "sk" {
		t.Fatalf("OSSSecretKey=%q", cfg.OSSSecretKey)
	}
	if cfg.OSSRegion != "ap-beijing" || cfg.OSSKeyPrefix != "im-test/marketplace" || cfg.OSSPathStyle {
		t.Fatalf("unexpected COS config: region=%q prefix=%q pathStyle=%v", cfg.OSSRegion, cfg.OSSKeyPrefix, cfg.OSSPathStyle)
	}
	if cfg.OSSPublicEndpoint != "https://cdn.example.com" {
		t.Fatalf("OSSPublicEndpoint=%q", cfg.OSSPublicEndpoint)
	}
	if !cfg.OSSPublicPathStyle {
		t.Fatal("OSSPublicPathStyle=false want true")
	}
	if cfg.OSSSigningHost != "my-bucket.cos.ap-beijing.myqcloud.com" {
		t.Fatalf("OSSSigningHost=%q", cfg.OSSSigningHost)
	}
	if !cfg.OSSDownloadSigned {
		t.Fatal("OSSDownloadSigned=false want true")
	}
	if cfg.MaxUploadMB != 50 {
		t.Fatalf("MaxUploadMB=%d want=50", cfg.MaxUploadMB)
	}
}

// TestDevAuthSpaceRoleZeroSurvives is the whole reason DEV_AUTH_SPACE_ROLE is
// not parsed by envInt: that helper treats <= 0 as unset and would promote a
// deliberately-configured plain member to the owner default, silently making
// the non-reviewer path unreachable in local development.
func TestDevAuthSpaceRoleZeroSurvives(t *testing.T) {
	t.Setenv("MYSQL_DSN", "test-dsn")
	t.Setenv("DEV_AUTH_SPACE_ROLE", "0")
	if got := Load().DevAuthSpaceRole; got != 0 {
		t.Fatalf("DevAuthSpaceRole=%d want=0 (an explicit member must not be promoted)", got)
	}
}

func TestDevAuthSpaceRoleDefaults(t *testing.T) {
	t.Setenv("MYSQL_DSN", "test-dsn")
	t.Setenv("DEV_AUTH_SPACE_ROLE", "")
	if got := Load().DevAuthSpaceRole; got != DefaultDevAuthSpaceRole {
		t.Fatalf("DevAuthSpaceRole=%d want=%d", got, DefaultDevAuthSpaceRole)
	}
}

func TestDevAuthSpaceRoleFromEnv(t *testing.T) {
	t.Setenv("MYSQL_DSN", "test-dsn")
	t.Setenv("DEV_AUTH_SPACE_ROLE", "1")
	if got := Load().DevAuthSpaceRole; got != 1 {
		t.Fatalf("DevAuthSpaceRole=%d want=1", got)
	}
}

// An unparseable value must not resolve to the owner default. It loads as an
// out-of-range sentinel so ValidateAPI fails the boot.
func TestDevAuthSpaceRoleUnparseableFailsValidation(t *testing.T) {
	t.Setenv("MYSQL_DSN", "test-dsn")
	t.Setenv("DEV_AUTH_SPACE_ROLE", "admin")
	cfg := Load()
	if cfg.DevAuthSpaceRole == DefaultDevAuthSpaceRole {
		t.Fatal("DevAuthSpaceRole silently fell back to the owner default on an unparseable value")
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("ValidateAPI() error=nil want DEV_AUTH_SPACE_ROLE error")
	}
}

func TestOctoNotifyDefaults(t *testing.T) {
	t.Setenv("MYSQL_DSN", "test-dsn")
	t.Setenv("OCTO_NOTIFY_TIMEOUT", "")
	t.Setenv("OCTO_CARD_ACTION_MAX_SKEW", "")
	t.Setenv("OCTO_MARKETPLACE_INTERNAL_TOKEN", "")
	t.Setenv("OCTO_MARKETPLACE_CARD_ACTION_SECRET", "")
	cfg := Load()
	if cfg.OctoNotifyTimeout != 3*time.Second {
		t.Fatalf("OctoNotifyTimeout=%v want=3s", cfg.OctoNotifyTimeout)
	}
	if cfg.OctoCardActionMaxSkew != 5*time.Minute {
		t.Fatalf("OctoCardActionMaxSkew=%v want=5m", cfg.OctoCardActionMaxSkew)
	}
	if cfg.OctoInternalToken != "" || cfg.OctoCardActionSecret != "" {
		t.Fatal("octo secrets must default to blank (surface disabled), never to a built-in value")
	}
}

func TestValidateAPIOctoSecrets(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
		wantSub string // non-empty: wantErr=true and error must contain this substring
	}{
		{name: "fully unconfigured is the safe rollout baseline (both surfaces closed, boot OK)",
			mutate: func(*Config) {}},
		{name: "fully configured boots",
			mutate: func(c *Config) {
				c.OctoAPIURL = "https://octo.example.com"
				c.OctoInternalToken = tokA
				c.OctoCardActionSecret = tokB
			}},
		// (a) OCTO_API_URL without INTERNAL_TOKEN: review endpoints return 200
		// but approval cards never dispatch. Warned at router startup, NOT a
		// boot error (there is a legitimate window where the URL is provisioned
		// ahead of the credential rollout).
		{name: "url without internal token warns but does not fail boot",
			mutate: func(c *Config) {
				c.OctoAPIURL = "https://octo.example.com"
			}},
		// (c) INTERNAL_TOKEN without CARD_ACTION_SECRET: cards go out but every
		// admin click 401s. Warned at router startup, NOT a boot error (rolling
		// out dispatch before the callback secret is the documented "safe"
		// phased rollout: it keeps the endpoint closed rather than open).
		{name: "internal token without card secret warns but does not fail boot",
			mutate: func(c *Config) {
				c.OctoInternalToken = tokA
			}},
		// (b) CARD_ACTION_SECRET without INTERNAL_TOKEN: signatures verify, then
		// operator-role lookup 503s on every click → DLQ death spiral. Fail boot.
		{name: "card secret without internal token fails boot",
			mutate: func(c *Config) {
				c.OctoCardActionSecret = tokB
			}, wantErr: true, wantSub: "OCTO_MARKETPLACE_CARD_ACTION_SECRET requires OCTO_MARKETPLACE_INTERNAL_TOKEN"},
		// (b') CARD_ACTION_SECRET without OCTO_API_URL: signature verifies but
		// role lookup has no endpoint → 503 forever. Fail boot (same DLQ class).
		{name: "card secret without URL fails boot",
			mutate: func(c *Config) {
				c.OctoCardActionSecret = tokB
				c.OctoInternalToken = tokA
				c.OctoAPIURL = ""
			}, wantErr: true, wantSub: "OCTO_MARKETPLACE_CARD_ACTION_SECRET requires OCTO_API_URL"},
		{name: "only internal token configured (no url, no card secret) boots",
			mutate: func(c *Config) {
				c.OctoInternalToken = tokA
			}},
		{name: "short internal token", mutate: func(c *Config) {
			c.OctoInternalToken = tokShort
		}, wantErr: true, wantSub: "at least 32 bytes"},
		{name: "short card action secret", mutate: func(c *Config) {
			c.OctoInternalToken = tokA
			c.OctoCardActionSecret = tokShort
		}, wantErr: true, wantSub: "at least 32 bytes"},
		{name: "reused secret across surfaces", mutate: func(c *Config) {
			c.OctoInternalToken = tokA
			c.OctoCardActionSecret = tokA
		}, wantErr: true, wantSub: "must not reuse"},
		{name: "dev space role above range", mutate: func(c *Config) {
			c.DevAuthSpaceRole = 3
		}, wantErr: true},
		{name: "dev space role below range", mutate: func(c *Config) {
			c.DevAuthSpaceRole = -1
		}, wantErr: true},
		{name: "dev space role member", mutate: func(c *Config) {
			c.DevAuthSpaceRole = 0
		}},
		{name: "dev space role owner", mutate: func(c *Config) {
			c.DevAuthSpaceRole = 2
		}},
		// Skew upper bound: anything over 15m is rejected to keep the replay
		// window bounded. The default 5m is well within the ceiling; 15m itself
		// is the hard cap (chosen to absorb a badly-skewed node clock after
		// restart without allowing multi-hour replay).
		{name: "skew at hard cap (15m) boots", mutate: func(c *Config) {
			c.OctoCardActionMaxSkew = 15 * time.Minute
		}},
		{name: "skew over hard cap (16m) fails boot", mutate: func(c *Config) {
			c.OctoCardActionMaxSkew = 16 * time.Minute
		}, wantErr: true, wantSub: "OCTO_CARD_ACTION_MAX_SKEW"},
		{name: "skew far over cap (720h) fails boot", mutate: func(c *Config) {
			c.OctoCardActionMaxSkew = 720 * time.Hour
		}, wantErr: true, wantSub: "OCTO_CARD_ACTION_MAX_SKEW"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := buildValid()
			tt.mutate(&cfg)
			err := cfg.ValidateAPI()
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateAPI() error=%v wantErr=%v", err, tt.wantErr)
			}
			if err != nil {
				if tt.wantSub != "" && !strings.Contains(err.Error(), tt.wantSub) {
					t.Fatalf("ValidateAPI() error=%v want substring %q", err, tt.wantSub)
				}
				if strings.Contains(err.Error(), tokA) || strings.Contains(err.Error(), tokShort) {
					t.Fatalf("ValidateAPI() error leaks a secret value: %v", err)
				}
			}
		})
	}
}
