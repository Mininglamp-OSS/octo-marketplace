package config

import (
	"testing"
	"time"
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
	validParse := func(c Config) Config {
		c.SkillParseTimeout = time.Minute
		c.SkillParseStaleTimeout = 5 * time.Minute
		return c
	}
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "valid", cfg: validParse(Config{MySQLDSN: "dsn", APIPort: "8092"})},
		{name: "missing dsn", cfg: validParse(Config{APIPort: "8092"}), wantErr: true},
		{name: "invalid port", cfg: validParse(Config{MySQLDSN: "dsn", APIPort: "0"}), wantErr: true},
		{name: "staleTimeout <= parseTimeout", cfg: Config{
			MySQLDSN: "dsn", APIPort: "8092",
			SkillParseTimeout: 5 * time.Minute, SkillParseStaleTimeout: 5 * time.Minute,
		}, wantErr: true},
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
	cfg := Config{MySQLDSN: "dsn", APIPort: "8092", AuthEnabled: true,
		SkillParseTimeout: time.Minute, SkillParseStaleTimeout: 5 * time.Minute}
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
