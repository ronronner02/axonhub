package conf

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/zap/zapcore"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/server/biz"
)

func TestLoaderReloadUsesFreshViper(t *testing.T) {
	configFile := writeTestConfig(t, `
log:
  level: info
`)
	loader := &Loader{configFile: configFile}

	initial, err := loader.Reload()
	if err != nil {
		t.Fatalf("initial Reload() error = %v", err)
	}
	if initial.Log.Level != zapcore.InfoLevel {
		t.Fatalf("initial log level = %s, want %s", initial.Log.Level, zapcore.InfoLevel)
	}

	if err := os.WriteFile(configFile, []byte(`
log:
  level: debug
`), 0o600); err != nil {
		t.Fatalf("rewrite test config: %v", err)
	}

	reloaded, err := loader.Reload()
	if err != nil {
		t.Fatalf("reloaded Reload() error = %v", err)
	}
	if reloaded.Log.Level != zapcore.DebugLevel {
		t.Fatalf("reloaded log level = %s, want %s", reloaded.Log.Level, zapcore.DebugLevel)
	}
}

func TestSSEKeepAliveConfig(t *testing.T) {
	configFile := writeTestConfig(t, `
server:
  sse_keep_alive:
    enabled: true
    interval: 30s
`)

	cfg, _, err := loadConfig(configFile)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if !cfg.APIServer.SSEKeepAlive.Enabled {
		t.Fatal("SSE keep-alive should be enabled")
	}
	if cfg.APIServer.SSEKeepAlive.Interval != 30*time.Second {
		t.Fatalf("SSE keep-alive interval = %s, want 30s", cfg.APIServer.SSEKeepAlive.Interval)
	}
}

func TestSSEKeepAliveDefaultsToDisabled(t *testing.T) {
	configFile := writeTestConfig(t, "")

	cfg, _, err := loadConfig(configFile)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.APIServer.SSEKeepAlive.Enabled {
		t.Fatal("SSE keep-alive should default to disabled")
	}
	if cfg.APIServer.SSEKeepAlive.Interval != 15*time.Second {
		t.Fatalf("SSE keep-alive interval = %s, want 15s", cfg.APIServer.SSEKeepAlive.Interval)
	}
}

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()

	configFile := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(configFile, []byte(contents), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return configFile
}

func TestInvalidEncryptedContentRecoveryConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		yaml string
		env  string
		want bool
	}{
		{name: "default_disabled"},
		{name: "yaml_enabled", yaml: "compatibility:\n  invalid_encrypted_content_recovery: true\n", want: true},
		{name: "env_enabled", env: "true", want: true},
		{name: "env_override_disabled", yaml: "compatibility:\n  invalid_encrypted_content_recovery: true\n", env: "false"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AXONHUB_COMPATIBILITY_INVALID_ENCRYPTED_CONTENT_RECOVERY", tt.env)
			cfg, _, err := loadConfig(writeTestConfig(t, tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Compatibility.InvalidEncryptedContentRecovery != tt.want {
				t.Fatalf("恢复开关 = %v，期望 %v", cfg.Compatibility.InvalidEncryptedContentRecovery, tt.want)
			}
		})
	}
}

func TestCompatibilityConfigReachesSystemService(t *testing.T) {
	cfg, _, err := loadConfig(writeTestConfig(t, "compatibility:\n  invalid_encrypted_content_recovery: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	var service *biz.SystemService
	app := fx.New(
		fx.NopLogger,
		fx.Provide(func() Config { return cfg }, func() *ent.Client { return nil }, biz.NewSystemService),
		fx.Populate(&service),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	if !service.CompatibilityConfig.InvalidEncryptedContentRecovery {
		t.Fatal("启动配置必须传入业务服务")
	}
}

func TestAnthropicStreamRecoveryChannelsConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		yaml string
		env  string
		want []string
	}{
		{name: "default_disabled", want: []string{}},
		{
			name: "yaml_enabled",
			yaml: "compatibility:\n  anthropic_stream_recovery_channels:\n    - linxi\n    - 100x\n",
			want: []string{"linxi", "100x"},
		},
		{
			name: "env_enabled",
			env:  `["17","18"]`,
			want: []string{"17", "18"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AXONHUB_COMPATIBILITY_ANTHROPIC_STREAM_RECOVERY_CHANNELS", tt.env)
			cfg, _, err := loadConfig(writeTestConfig(t, tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Compatibility.AnthropicStreamRecoveryChannels, tt.want) {
				t.Fatalf("AnthropicStreamRecoveryChannels = %#v, want %#v", cfg.Compatibility.AnthropicStreamRecoveryChannels, tt.want)
			}
		})
	}
}
