# Caddy Nacos Config Adapter — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Caddy config adapter that reads Caddy configuration from Nacos with hot-reload and multi-format support.

**Architecture:** Package `caddynacos` implements the `caddyconfig.Adapter` interface, registered as `"nacos"`. On `Adapt()`, it reads configuration from Nacos (multiple DATA_IDs), auto-detects format (JSON/YAML/TOML/Caddyfile), assembles into Caddy JSON, and starts push-based listeners for hot-reload.

**Tech Stack:** Go, Nacos SDK v2 (`nacos-group/nacos-sdk-go/v2`), Caddy v2.9.x, `log/slog`, `gopkg.in/yaml.v3`, `github.com/BurntSushi/toml`

## Global Constraints

- Adapter name: `"nacos"` (registered via `caddyconfig.RegisterAdapter`)
- Module path: `github.com/hxgm/caddynacos`
- Namespace: `runtime.GOOS == "windows"` → `"prod"`, else `"dev"`; config override allowed
- DATA_IDs: multi-key pattern (version, config, config.admin, config.logging, config.storage, config.apps, config.apps.http.servers.*.routes)
- Formats: JSON/YAML/TOML/Caddyfile auto-detected per DATA_ID
- Hot-reload: Nacos `ListenConfig` push-based (no polling), calls `caddy.Load()`
- Logging: `log/slog` outputting to stderr
- Package name: `caddynacos`
- var func: `var GetNacosConfig func() *NacosConfig` — users set in init()
- Go minimum version: 1.22
- Caddy minimum version: v2.9.1

---
### Task 1: Project scaffold

**Files:**
- Create: `go.mod`
- Create: `nacos.go` (package declaration only)
- Create: `format.go` (package declaration only)

**Interfaces:**
- Consumes: nothing
- Produces: compilable Go module with empty package

- [ ] **Step 1: Create go.mod with module path and dependencies**

```bash
go mod init github.com/hxgm/caddynacos
```

Then write `go.mod` with these require blocks:

```go
module github.com/hxgm/caddynacos

go 1.22

require (
	github.com/BurntSushi/toml v1.4.0
	github.com/caddyserver/caddy/v2 v2.9.1
	github.com/nacos-group/nacos-sdk-go/v2 v2.2.7
	gopkg.in/yaml.v3 v3.0.1
)
```

- [ ] **Step 2: Create nacos.go with package declaration**

```go
package caddynacos
```

```bash
cat > nacos.go << 'EOF'
package caddynacos
EOF
```

- [ ] **Step 3: Create format.go with package declaration**

```bash
cat > format.go << 'EOF'
package caddynacos
EOF
```

- [ ] **Step 4: Run `go mod tidy` to resolve dependencies**

```bash
go mod tidy
```

Expected: exits cleanly, `go.sum` created. May take a while to download Caddy and Nacos SDK deps.

- [ ] **Step 5: Verify compilation**

```bash
go build ./...
```

Expected: no errors, no output.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum nacos.go format.go
git commit -m "chore: scaffold caddynacos module"
```

### Task 2: Format detection and conversion

**Files:**
- Modify: `format.go`
- Create: `format_test.go`

**Interfaces:**
- Produces: `detectFormat(data string) ConfigFormat`, `convertToJSON(data string, logger *slog.Logger) ([]byte, error)`

- [ ] **Step 1: Write format.go with detection and conversion**

Write `format.go`:

```go
package caddynacos

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"gopkg.in/yaml.v3"
)

// ConfigFormat represents the detected configuration format.
type ConfigFormat int

const (
	FormatJSON ConfigFormat = iota
	FormatYAML
	FormatTOML
	FormatCaddyfile
)

// detectFormat detects the configuration format from the raw content string.
func detectFormat(data string) ConfigFormat {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return FormatJSON
	}

	first := trimmed[0]

	// JSON: starts with { or [
	if first == '{' || first == '[' {
		return FormatJSON
	}

	lines := strings.SplitN(trimmed, "\n", 10)

	// TOML: first non-empty line starts with [...]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			return FormatTOML
		}
		break
	}

	// YAML: contains "key: value" pattern in first content line
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		// YAML key: value or key:
		if strings.Contains(line, ": ") || strings.HasSuffix(line, ":") {
			return FormatYAML
		}
		break
	}

	// Everything else is treated as Caddyfile
	return FormatCaddyfile
}

// convertToJSON converts config data in any supported format to Caddy JSON bytes.
func convertToJSON(data string, logger *slog.Logger) ([]byte, error) {
	format := detectFormat(data)

	switch format {
	case FormatJSON:
		// Validate it's valid JSON
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return raw, nil

	case FormatYAML:
		var parsed any
		if err := yaml.Unmarshal([]byte(data), &parsed); err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		// Clean up YAML-native types to JSON-safe ones
		parsed = yamlToJSONSafe(parsed)
		result, err := json.Marshal(parsed)
		if err != nil {
			return nil, fmt.Errorf("YAML to JSON marshal: %w", err)
		}
		return result, nil

	case FormatTOML:
		var parsed map[string]any
		if _, err := toml.Decode(data, &parsed); err != nil {
			return nil, fmt.Errorf("invalid TOML: %w", err)
		}
		result, err := json.Marshal(parsed)
		if err != nil {
			return nil, fmt.Errorf("TOML to JSON marshal: %w", err)
		}
		return result, nil

	case FormatCaddyfile:
		adapter := caddyconfig.GetAdapter("caddyfile")
		if adapter == nil {
			return nil, fmt.Errorf("caddyfile adapter not available")
		}
		result, warnings, err := adapter.Adapt([]byte(data), nil)
		if err != nil {
			return nil, fmt.Errorf("caddyfile adapt: %w", err)
		}
		for _, w := range warnings {
			logger.Warn("caddyfile adapter warning", "msg", w.Message)
		}
		return result, nil

	default:
		return nil, fmt.Errorf("unknown config format")
	}
}

// yamlToJSONSafe converts YAML-native types (map[any]any) to JSON-safe types (map[string]any).
func yamlToJSONSafe(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = yamlToJSONSafe(val)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[fmt.Sprintf("%v", k)] = yamlToJSONSafe(val)
		}
		return m
	case []any:
		for i, val := range x {
			x[i] = yamlToJSONSafe(val)
		}
		return x
	default:
		return v
	}
}

// FormatNames for logging and error messages.
var FormatNames = map[ConfigFormat]string{
	FormatJSON:      "JSON",
	FormatYAML:      "YAML",
	FormatTOML:      "TOML",
	FormatCaddyfile: "Caddyfile",
}
```

- [ ] **Step 2: Write format_test.go with format detection tests**

```go
package caddynacos

import (
	"log/slog"
	"os"
	"testing"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

func TestDetectFormat_JSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		fmt  ConfigFormat
	}{
		{"object", `{"key": "value"}`, FormatJSON},
		{"array", `[{"key": "value"}]`, FormatJSON},
		{"nested", "{\n  \"server\": {\n    \"listen\": [\":80\"]\n  }\n}", FormatJSON},
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
		{"keyvalue", "title = \"config\"\n[admin]\nlisten = \"localhost:2019\"", FormatTOML},
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
		{"domain", "localhost:8080 {\n\trespond \"Hello\"\n}", FormatCaddyfile},
		{"global", "{\n\tdebug\n}\nlocalhost:8080 {\n\trespond \"Hello\"\n}", FormatCaddyfile},
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
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if parsed["key"] != "value" {
		t.Errorf("expected key=value, got %v", parsed["key"])
	}
}

func TestConvertToJSON_TOML(t *testing.T) {
	input := "[server]\nlisten = \":80\"\n[http]\nport = 8080"
	result, err := convertToJSON(input, testLogger)
	if err != nil {
		t.Fatalf("convertToJSON() error = %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
}

func TestConvertToJSON_Empty(t *testing.T) {
	result, err := convertToJSON("", testLogger)
	if err != nil {
		t.Fatalf("convertToJSON() error = %v", err)
	}
	if string(result) != "null" && string(result) != "" {
		t.Logf("empty input produced: %s", string(result))
	}
}
```

- [ ] **Step 3: Run format tests**

```bash
go test -v -run TestDetect -count=1 ./...
go test -v -run TestConvertToJSON -count=1 ./...
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add format.go format_test.go
git commit -m "feat: add format detection and conversion (JSON/YAML/TOML/Caddyfile)"
```

### Task 3: Core Nacos adapter (nacos.go)

**Files:**
- Modify: `nacos.go`
- Create: `nacos_test.go`

**Interfaces:**
- Consumes: `detectFormat()`, `convertToJSON()`, `FormatNames` from format.go
- Produces: `Adapter` struct, `Adapt()` method, `GetNacosConfig` var func, `buildConfig()`, `startListeners()`

- [ ] **Step 1: Write nacos.go with full adapter implementation**

```go
package caddynacos

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// GetNacosConfig is a var func pattern.
// Override in init() to hardcode Nacos connection info without a config file.
var GetNacosConfig func() *NacosConfig

// NacosConfig holds hardcoded Nacos connection parameters for the var func pattern.
type NacosConfig struct {
	ServerAddr string
	ServerPort uint64
	Username   string
	Password   string
	DataIDs    []string
	Group      string
}

// AdapterConfig is deserialized from nacos.json.
type AdapterConfig struct {
	ServerAddr string   `json:"serverAddr"`
	ServerPort uint64   `json:"serverPort"`
	Username   string   `json:"username,omitempty"`
	Password   string   `json:"password,omitempty"`
	Namespace  string   `json:"namespace,omitempty"`
	DataIDs    []string `json:"dataIds"`
	Group      string   `json:"group,omitempty"`
}

func (cfg *AdapterConfig) fillFromNacosConfig(nc *NacosConfig) {
	if cfg.ServerAddr == "" {
		cfg.ServerAddr = nc.ServerAddr
	}
	if cfg.ServerPort == 0 {
		cfg.ServerPort = nc.ServerPort
	}
	if cfg.Username == "" {
		cfg.Username = nc.Username
	}
	if cfg.Password == "" {
		cfg.Password = nc.Password
	}
	if len(cfg.DataIDs) == 0 {
		cfg.DataIDs = nc.DataIDs
	}
	if cfg.Group == "" {
		cfg.Group = nc.Group
	}
}

// logger is the package-level slog logger outputting to stderr.
var logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
	Level: slog.LevelInfo,
}))

func init() {
	caddyconfig.RegisterAdapter("nacos", Adapter{})
}

// Adapter implements caddyconfig.Adapter for Nacos.
type Adapter struct{}

var _ caddyconfig.Adapter = (*Adapter)(nil)

// Adapt reads configuration from Nacos and returns Caddy JSON.
func (a Adapter) Adapt(body []byte, options map[string]any) ([]byte, []caddyconfig.Warning, error) {
	// Parse config file
	cfg := &AdapterConfig{
		Group: "DEFAULT_GROUP",
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, cfg); err != nil {
			return nil, nil, fmt.Errorf("parse nacos adapter config: %w", err)
		}
	}

	// Apply var func override (if set)
	if GetNacosConfig != nil {
		nc := GetNacosConfig()
		if nc != nil {
			cfg.fillFromNacosConfig(nc)
		}
	}

	// Validate required fields
	if cfg.ServerAddr == "" {
		return nil, nil, fmt.Errorf("nacos adapter: serverAddr is required (set in config file or GetNacosConfig)")
	}
	if cfg.ServerPort == 0 {
		cfg.ServerPort = 8848
	}
	if len(cfg.DataIDs) == 0 {
		return nil, nil, fmt.Errorf("nacos adapter: at least one dataId is required")
	}

	// Resolve namespace
	namespace := resolveNamespace(cfg.Namespace)
	logger.Info("nacos adapter starting",
		"server", fmt.Sprintf("%s:%d", cfg.ServerAddr, cfg.ServerPort),
		"namespace", namespace,
		"dataIds", cfg.DataIDs,
		"group", cfg.Group,
	)

	// Create Nacos config client
	clientConfig := constant.ClientConfig{
		NamespaceId:         namespace,
		TimeoutMs:           5000,
		NotLoadCacheAtStart: true,
		LogDir:              os.TempDir() + "/nacos-log",
		CacheDir:            os.TempDir() + "/nacos-cache",
		LogLevel:            "warn",
		Username:            cfg.Username,
		Password:            cfg.Password,
	}
	serverConfigs := []constant.ServerConfig{
		{
			IpAddr: cfg.ServerAddr,
			Port:   cfg.ServerPort,
		},
	}

	client, err := clients.NewConfigClient(
		vo.NacosClientParam{
			ClientConfig:  &clientConfig,
			ServerConfigs: serverConfigs,
		},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create nacos config client: %w", err)
	}

	// Build initial configuration
	configJSON, err := buildConfig(client, cfg.DataIDs, cfg.Group)
	if err != nil {
		return nil, nil, fmt.Errorf("build config from nacos: %w", err)
	}

	// Start hot-reload listeners
	startListeners(client, cfg, configJSON)

	return configJSON, nil, nil
}

// resolveNamespace returns the Nacos namespace based on runtime.GOOS.
func resolveNamespace(override string) string {
	if override != "" {
		return override
	}
	if runtime.GOOS == "windows" {
		return "prod"
	}
	return "dev"
}

// buildConfig reads all DATA_IDs from Nacos and assembles a Caddy JSON config.
func buildConfig(client clientInterface, dataIDs []string, group string) ([]byte, error) {
	// Read version (for logging/tracking)
	version, _ := getConfigValue(client, "version", group)
	if version != "" {
		logger.Info("nacos config version", "version", version)
	}

	// Build base config
	configData, err := getConfigValue(client, "config", group)
	if err != nil {
		return nil, fmt.Errorf("read 'config' from nacos: %w", err)
	}

	config := caddy.Config{}
	if configData != "" {
		configJSON, convErr := convertToJSON(configData, logger)
		if convErr != nil {
			return nil, fmt.Errorf("convert 'config' to json: %w", convErr)
		}
		if err := json.Unmarshal(configJSON, &config); err != nil {
			return nil, fmt.Errorf("unmarshal 'config' into caddy.Config: %w", err)
		}
	}

	// Merge admin config
	if data, ok := getOptionalConfigValue(client, "config.admin", group); ok {
		jsonData, convErr := convertToJSON(data, logger)
		if convErr == nil {
			if config.Admin == nil {
				config.Admin = &caddy.AdminConfig{}
			}
			json.Unmarshal(jsonData, config.Admin)
		}
	}

	// Merge logging config
	if data, ok := getOptionalConfigValue(client, "config.logging", group); ok {
		jsonData, convErr := convertToJSON(data, logger)
		if convErr == nil {
			if config.Logging == nil {
				config.Logging = &caddy.Logging{}
			}
			json.Unmarshal(jsonData, config.Logging)
		}
	}

	// Merge storage config
	if data, ok := getOptionalConfigValue(client, "config.storage", group); ok {
		jsonData, convErr := convertToJSON(data, logger)
		if convErr == nil {
			config.StorageRaw = json.RawMessage(jsonData)
		}
	}

	// Merge apps config
	if data, ok := getOptionalConfigValue(client, "config.apps", group); ok {
		jsonData, convErr := convertToJSON(data, logger)
		if convErr == nil {
			config.AppsRaw = caddy.ModuleMap{}
			json.Unmarshal(jsonData, &config.AppsRaw)
		}
	}

	// Merge http server routes
	if config.AppsRaw != nil {
		if httpRaw, hasHTTP := config.AppsRaw["http"]; hasHTTP {
			httpApp := caddyhttp.App{}
			if err := json.Unmarshal(httpRaw, &httpApp); err == nil {
				changed := false
				if httpApp.Servers != nil {
					for serverKey := range httpApp.Servers {
						routesKey := "config.apps.http.servers." + serverKey + ".routes"
						values, _ := getOptionalConfigValues(client, routesKey, group)
						if len(values) > 0 {
							if httpApp.Servers[serverKey].Routes == nil {
								httpApp.Servers[serverKey].Routes = make([]caddyhttp.Route, 0)
							}
							for _, routeData := range values {
								jsonData, convErr := convertToJSON(routeData, logger)
								if convErr != nil {
									logger.Error("convert route to json", "error", convErr, "dataId", routesKey)
									continue
								}
								var route caddyhttp.Route
								if err := json.Unmarshal(jsonData, &route); err == nil {
									httpApp.Servers[serverKey].Routes = append(
										httpApp.Servers[serverKey].Routes, route,
									)
									changed = true
								}
							}
						}
					}
				}
				if changed {
					var warnings []caddyconfig.Warning
					config.AppsRaw["http"] = caddyconfig.JSON(httpApp, &warnings)
					for _, w := range warnings {
						logger.Warn("re-encode http app", "msg", w.Message)
					}
				}
			}
		}
	}

	return json.Marshal(config)
}

// clientInterface abstracts the Nacos config client for testability.
type clientInterface interface {
	GetConfig(param vo.ConfigParam) (string, error)
	ListenConfig(param vo.ConfigParam) error
	CancelListenConfig(param vo.ConfigParam) error
}

// getConfigValue reads a single config value from Nacos.
func getConfigValue(client clientInterface, dataID, group string) (string, error) {
	return client.GetConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  group,
	})
}

// getOptionalConfigValue reads a config value, returning false if not found.
func getOptionalConfigValue(client clientInterface, dataID, group string) (string, bool) {
	val, err := client.GetConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  group,
	})
	if err != nil || val == "" {
		return "", false
	}
	return val, true
}

// getOptionalConfigValues returns all matching config values for a dataID.
// Note: Nacos SDK returns a single value per dataID. Multi-value pattern
// matching (like mysql adapter's "ORDER BY CREATED DESC") is not supported
// in Nacos. We return a single-element slice for interface compatibility.
func getOptionalConfigValues(client clientInterface, dataID, group string) ([]string, bool) {
	val, ok := getOptionalConfigValue(client, dataID, group)
	if !ok {
		return nil, false
	}
	return []string{val}, true
}

// mu guards concurrent reloads from multiple Nacos callbacks.
var reloadMu sync.Mutex

// lastConfigJSON caches the last successfully loaded config to avoid redundant reloads.
var lastConfigJSON []byte

// startListeners registers Nacos push listeners on all DATA_IDs.
func startListeners(client clientInterface, cfg *AdapterConfig, initialConfig []byte) {
	lastConfigJSON = initialConfig

	for _, dataID := range cfg.DataIDs {
		did := dataID // capture for closure
		go func() {
			err := client.ListenConfig(vo.ConfigParam{
				DataId: did,
				Group:  cfg.Group,
				OnChange: func(namespace, group, dataId, data string) {
					logger.Info("nacos config changed",
						"dataId", dataId, "group", group, "namespace", namespace,
					)

					reloadMu.Lock()
					defer reloadMu.Unlock()

					newConfig, err := buildConfig(client, cfg.DataIDs, cfg.Group)
					if err != nil {
						logger.Error("rebuild config from nacos failed", "error", err)
						return
					}

					// Check if config actually changed
					if len(newConfig) == len(lastConfigJSON) &&
						string(newConfig) == string(lastConfigJSON) {
						logger.Debug("config unchanged, skipping reload")
						return
					}

					if err := caddy.Load(newConfig, false); err != nil {
						logger.Error("caddy.Load failed", "error", err)
						return
					}

					lastConfigJSON = newConfig
					logger.Info("caddy config reloaded from nacos",
						"dataId", dataId, "version", getVersionDisplay(client, cfg.Group),
					)
				},
			})
			if err != nil {
				logger.Error("listen nacos config failed", "dataId", did, "error", err)
			}
		}()
	}

	logger.Info("nacos listeners started", "count", len(cfg.DataIDs))
}

// getVersionDisplay reads the version DATA_ID for logging.
func getVersionDisplay(client clientInterface, group string) string {
	v, _ := getConfigValue(client, "version", group)
	return v
}
```

- [ ] **Step 2: Write nacos_test.go with adapter tests**

```go
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
```

- [ ] **Step 3: Run all tests**

```bash
go test -v -count=1 ./...
```

Expected: all tests pass (format tests + nacos adapter tests with mock client).

- [ ] **Step 4: Commit**

```bash
git add nacos.go nacos_test.go
git commit -m "feat: add nacos config adapter with hot-reload"
```

### Task 4: Verification — xcaddy build smoke test

**Files:**
- No new files. Verifies all previous tasks produce a buildable xcaddy module.

**Interfaces:**
- Consumes: complete `nacos.go`, `format.go`, `go.mod`, `go.sum`

- [ ] **Step 1: Build the module standalone**

```bash
go build ./...
```

Expected: exits cleanly, no output.

- [ ] **Step 2: Vet the module**

```bash
go vet ./...
```

Expected: no warnings or errors.

- [ ] **Step 3: Verify xcaddy can build with this adapter (if xcaddy installed)**

```bash
# Check if xcaddy is available
which xcaddy || echo "xcaddy not installed, skipping integration test"
```

If xcaddy is installed:
```bash
xcaddy build v2.9.1 --with github.com/hxgm/caddynacos=.
```

Expected: produces a `caddy` binary in current directory.

- [ ] **Step 4: Verify the built caddy binary lists the adapter**

```bash
./caddy list-modules | grep nacos
```

Expected: output contains `caddy.adapters.nacos`.

If xcaddy is not available, manually verify the adapter registration would work:
```bash
go run -mod=mod github.com/caddyserver/xcaddy/cmd/xcaddy build v2.9.1 --with github.com/hxgm/caddynacos=. 2>&1 || echo "Need xcaddy install for full test"
```

- [ ] **Step 5: Run full test suite**

```bash
go test -v -count=1 -race ./...
```

Expected: all tests pass with race detection enabled.

- [ ] **Step 6: Final commit (if any fixes applied)**

```bash
git add -A
git commit -m "chore: finalize caddy-nacos adapter implementation"
```
