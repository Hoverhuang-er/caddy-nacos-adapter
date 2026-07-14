package caddynacos

import (
	"encoding/base64"
	"encoding/json" // 保留用于 json.RawMessage（Caddy 类型兼容）
	jsonv2 "encoding/json/v2"
	"log/slog"
	"os"
	"testing"

	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// testLogger 是测试用的 slog 日志器。
var testLogger = slog.New(slog.NewTextHandler(
	os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

// ---------------------------------------------------------------------------
// 格式检测测试
// ---------------------------------------------------------------------------

func TestDetectFormat_JSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		fmt  ConfigFormat
	}{
		{"object", `{"key": "value"}`, FormatJSON},
		{"array", `[{"key": "value"}]`, FormatJSON},
		{"nested",
			"{\n  \"server\": {\n    \"listen\": [\":80\"]\n  }\n}",
			FormatJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectFormat(tt.data); got != tt.fmt {
				t.Errorf("detectFormat() = %v, want %v", got, tt.fmt)
			}
		})
	}
}

func TestDetectFormat_YAML(t *testing.T) {
	tests := []struct {
		name string
		data string
		fmt  ConfigFormat
	}{
		{"simple", "key: value\nfoo: bar", FormatYAML},
		{"nested", "server:\n  listen: \":80\"", FormatYAML},
		{"with comments", "# comment\nkey: value", FormatYAML},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectFormat(tt.data); got != tt.fmt {
				t.Errorf("detectFormat() = %v, want %v", got, tt.fmt)
			}
		})
	}
}

func TestDetectFormat_TOML(t *testing.T) {
	tests := []struct {
		name string
		data string
		fmt  ConfigFormat
	}{
		{"table", "[server]\nlisten = \":80\"", FormatTOML},
		{"keyvalue",
			"title = \"config\"\n[admin]\nlisten = \"localhost:2019\"",
			FormatTOML},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectFormat(tt.data); got != tt.fmt {
				t.Errorf("detectFormat() = %v, want %v", got, tt.fmt)
			}
		})
	}
}

func TestDetectFormat_Caddyfile(t *testing.T) {
	tests := []struct {
		name string
		data string
		fmt  ConfigFormat
	}{
		{"domain",
			"localhost:8080 {\n\trespond \"Hello\"\n}",
			FormatCaddyfile},
		{"global",
			"{\n\tdebug\n}\nlocalhost:8080 {\n\trespond \"Hello\"\n}",
			FormatCaddyfile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectFormat(tt.data); got != tt.fmt {
				t.Errorf("detectFormat() = %v, want %v", got, tt.fmt)
			}
		})
	}
}

func TestConvertToJSON_JSON(t *testing.T) {
	input := `{"key": "value", "num": 42}`
	result, err := convertToJSON(input, testLogger)
	if err != nil {
		t.Fatalf("convertToJSON() error = %v", err)
	}
	if string(result) != input {
		t.Errorf("convertToJSON() = %s, want %s", string(result), input)
	}
}

func TestConvertToJSON_YAML(t *testing.T) {
	input := "key: value\nnum: 42"
	result, err := convertToJSON(input, testLogger)
	if err != nil {
		t.Fatalf("convertToJSON() error = %v", err)
	}
	var parsed map[string]any
	if err := jsonv2.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	if parsed["key"] != "value" {
		t.Errorf("期望 key=value, 得到 %v", parsed["key"])
	}
}

func TestConvertToJSON_TOML(t *testing.T) {
	input := "[server]\nlisten = \":80\"\n[http]\nport = 8080"
	result, err := convertToJSON(input, testLogger)
	if err != nil {
		t.Fatalf("convertToJSON() error = %v", err)
	}
	var parsed map[string]any
	if err := jsonv2.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
}

func TestConvertToJSON_Empty(t *testing.T) {
	result, err := convertToJSON("", testLogger)
	if err != nil {
		t.Fatalf("convertToJSON() error = %v", err)
	}
	if string(result) != "null" && string(result) != "" {
		t.Logf("空输入产生: %s", string(result))
	}
}

// ---------------------------------------------------------------------------
// Mock Nacos 客户端
// ---------------------------------------------------------------------------

// mockNacosClient 实现了 clientInterface，用于测试。
type mockNacosClient struct {
	data map[string]string
}

func (m *mockNacosClient) GetConfig(
	param vo.ConfigParam,
) (string, error) {
	if val, ok := m.data[param.DataId]; ok {
		return val, nil
	}
	return "", nil
}

func (m *mockNacosClient) ListenConfig(
	param vo.ConfigParam,
) error {
	return nil
}

func (m *mockNacosClient) CancelListenConfig(
	param vo.ConfigParam,
) error {
	return nil
}

// ---------------------------------------------------------------------------
// Namespace 解析测试
// ---------------------------------------------------------------------------

func TestResolveNamespace(t *testing.T) {
	tests := []struct {
		name string
		ns   string
		want string
	}{
		{"自定义 namespace", "custom-ns", "custom-ns"},
		{"空值默认为 dev", "", "dev"},
		{"auto 默认为 dev", "auto", "dev"},
		{"public 映射为空", "public", ""},
		{"PUBLIC 映射为空", "PUBLIC", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveNamespace(tt.ns); got != tt.want {
				t.Errorf("resolveNamespace() = %v, want %v",
					got, tt.want)
			}
		})
	}
}

func TestResolveCredentialsFromEnv(t *testing.T) {
	orig := os.Getenv(EnvNacosAuth)
	defer os.Setenv(EnvNacosAuth, orig)

	t.Run("无环境变量", func(t *testing.T) {
		os.Unsetenv(EnvNacosAuth)
		u, p, ok := resolveCredentialsFromEnv("dev")
		if ok {
			t.Errorf("期望 ok=false, 得到 %v", ok)
		}
		if u != "" || p != "" {
			t.Errorf("期望空值, 得到 %q %q", u, p)
		}
	})

	t.Run("匹配 dev namespace", func(t *testing.T) {
		os.Setenv(EnvNacosAuth,
			"ZGV2OmRldnVzZXI6ZGV2cGFzcztwcm9kOnByb2R1c2VyOnByb2RwYXNz")
		u, p, ok := resolveCredentialsFromEnv("dev")
		if !ok {
			t.Fatalf("期望 ok=true, 得到 false")
		}
		if u != "devuser" || p != "devpass" {
			t.Errorf("期望 devuser/devpass, 得到 %q %q", u, p)
		}
	})

	t.Run("匹配 prod namespace", func(t *testing.T) {
		os.Setenv(EnvNacosAuth,
			"ZGV2OmRldnVzZXI6ZGV2cGFzcztwcm9kOnByb2R1c2VyOnByb2RwYXNz")
		u, p, ok := resolveCredentialsFromEnv("prod")
		if !ok {
			t.Fatalf("期望 ok=true, 得到 false")
		}
		if u != "produser" || p != "prodpass" {
			t.Errorf("期望 produser/prodpass, 得到 %q %q", u, p)
		}
	})

	t.Run("未匹配到 namespace", func(t *testing.T) {
		os.Setenv(EnvNacosAuth, "ZGV2OmRldnVzZXI6ZGV2cGFzcw==")
		u, p, ok := resolveCredentialsFromEnv("staging")
		if ok {
			t.Errorf("期望 ok=false, 得到 %v", ok)
		}
		if u != "" || p != "" {
			t.Errorf("期望空值, 得到 %q %q", u, p)
		}
	})

	t.Run("无效 base64", func(t *testing.T) {
		os.Setenv(EnvNacosAuth, "not-valid-base64!!!")
		u, p, ok := resolveCredentialsFromEnv("dev")
		if ok {
			t.Errorf("期望 ok=false, 得到 %v", ok)
		}
		if u != "" || p != "" {
			t.Errorf("期望空值, 得到 %q %q", u, p)
		}
	})

	t.Run("格式错误（缺少 password）", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString(
			[]byte("dev:user"))
		os.Setenv(EnvNacosAuth, encoded)
		u, p, ok := resolveCredentialsFromEnv("dev")
		if ok {
			t.Errorf("期望 ok=false, 得到 %v", ok)
		}
		if u != "" || p != "" {
			t.Errorf("期望空值, 得到 %q %q", u, p)
		}
	})
}

// ---------------------------------------------------------------------------
// 配置组装测试
// ---------------------------------------------------------------------------

func TestBuildConfig_JSON(t *testing.T) {
	client := &mockNacosClient{
		data: map[string]string{
			"version": "1",
			"config": `{"admin": {"listen": "localhost:2019"}, "logging": {"logs": {"default": {"level": "INFO"}}}}`,
		},
	}

	result, err := buildConfig(
		client, []string{"version", "config"}, "DEFAULT_GROUP")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}

	var parsed caddyConfigStub
	if err := jsonv2.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
}

func TestBuildConfig_YAML(t *testing.T) {
	client := &mockNacosClient{
		data: map[string]string{
			"version": "1",
			"config": "admin:\n  listen: \"localhost:2019\"\nlogging:\n  logs:\n    default:\n      level: INFO",
		},
	}

	result, err := buildConfig(
		client, []string{"version", "config"}, "DEFAULT_GROUP")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}

	var parsed caddyConfigStub
	if err := jsonv2.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
}

func TestBuildConfig_Empty(t *testing.T) {
	client := &mockNacosClient{
		data: map[string]string{
			"version": "0",
			"config":  "",
		},
	}

	result, err := buildConfig(
		client, []string{"version", "config"}, "DEFAULT_GROUP")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}

	var parsed caddyConfigStub
	if err := jsonv2.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
}

func TestBuildConfig_WithApps(t *testing.T) {
	configYAML := `apps:
  http:
    http_port: 80
    servers:
      srv0:
        listen:
          - ":80"
        routes:
          - handle:
              - handler: "static_response"
                body: "Hello"
            match:
              - host:
                  - "localhost"
            terminal: true`

	client := &mockNacosClient{
		data: map[string]string{
			"version": "2",
			"config":  configYAML,
		},
	}

	result, err := buildConfig(
		client, []string{"version", "config"}, "DEFAULT_GROUP")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}

	var parsed map[string]any
	if err := jsonv2.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 测试辅助类型
// ---------------------------------------------------------------------------

// caddyConfigStub 是用于验证 JSON 结构的最小桩类型。
type caddyConfigStub struct {
	Admin   *adminConfigStub `json:"admin,omitempty"`
	Logging *loggingStub     `json:"logging,omitempty"`
	Apps    json.RawMessage  `json:"apps,omitempty"`
}

type adminConfigStub struct {
	Listen string `json:"listen,omitempty"`
}

type loggingStub struct {
	Logs map[string]any `json:"logs,omitempty"`
}
