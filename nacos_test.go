package caddynacos

import (
	"encoding/json"
	"testing"

	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// mockNacosClient implements clientInterface for testing.
type mockNacosClient struct {
	data map[string]string
}

func (m *mockNacosClient) GetConfig(param vo.ConfigParam) (string, error) {
	if val, ok := m.data[param.DataId]; ok {
		return val, nil
	}
	return "", nil
}

func (m *mockNacosClient) ListenConfig(param vo.ConfigParam) error {
	return nil
}

func (m *mockNacosClient) CancelListenConfig(param vo.ConfigParam) error {
	return nil
}

func TestResolveNamespace(t *testing.T) {
	tests := []struct {
		name     string
		override string
		want     string
	}{
		{"override wins", "custom-ns", "custom-ns"},
		{"empty override defaults", "", "dev"}, // non-windows in test
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveNamespace(tt.override); got != tt.want {
				t.Errorf("resolveNamespace() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildConfig_JSON(t *testing.T) {
	client := &mockNacosClient{
		data: map[string]string{
			"version": "1",
			"config": `{"admin": {"listen": "localhost:2019"}, "logging": {"logs": {"default": {"level": "INFO"}}}}`,
		},
	}

	result, err := buildConfig(client, []string{"version", "config"}, "DEFAULT_GROUP")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}

	var parsed caddyConfigStub
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result is not valid Caddy JSON: %v", err)
	}
}

func TestBuildConfig_YAML(t *testing.T) {
	client := &mockNacosClient{
		data: map[string]string{
			"version": "1",
			"config": "admin:\n  listen: \"localhost:2019\"\nlogging:\n  logs:\n    default:\n      level: INFO",
		},
	}

	result, err := buildConfig(client, []string{"version", "config"}, "DEFAULT_GROUP")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}

	var parsed caddyConfigStub
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result is not valid Caddy JSON: %v", err)
	}
}

func TestBuildConfig_Empty(t *testing.T) {
	client := &mockNacosClient{
		data: map[string]string{
			"version": "0",
			"config":  "",
		},
	}

	result, err := buildConfig(client, []string{"version", "config"}, "DEFAULT_GROUP")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}

	// Should produce valid (empty) Caddy JSON
	var parsed caddyConfigStub
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
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

	result, err := buildConfig(client, []string{"version", "config"}, "DEFAULT_GROUP")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
}

// caddyConfigStub is a minimal stub for verifying JSON structure.
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
