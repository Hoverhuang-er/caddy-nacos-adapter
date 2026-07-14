package caddynacos

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
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

// EnvNacosAuth is the environment variable for base64-encoded Nacos credentials.
// Format (after decode): ns1:username1:password1;ns2:username2:password2
const EnvNacosAuth = "CADDY_NACOS_AUTH"

// resolveCredentialsFromEnv decodes and parses the CADDY_NACOS_AUTH env var,
// returning the username and password for the given namespace.
func resolveCredentialsFromEnv(namespace string) (username, password string, ok bool) {
	encoded := os.Getenv(EnvNacosAuth)
	if encoded == "" {
		return "", "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		logger.Warn("failed to decode "+EnvNacosAuth, "error", err)
		return "", "", false
	}

	pairs := strings.Split(string(decoded), ";")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, ":", 3)
		if len(parts) != 3 {
			logger.Warn("invalid credential entry in "+EnvNacosAuth, "entry", pair)
			continue
		}
		ns, user, pass := parts[0], parts[1], parts[2]
		if ns == namespace {
			return user, pass, true
		}
	}

	return "", "", false
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
		"server", net.JoinHostPort(cfg.ServerAddr, strconv.Itoa(int(cfg.ServerPort))),
		"namespace", namespace,
		"dataIds", cfg.DataIDs,
		"group", cfg.Group,
	)

	// Apply env var credentials for username/password (if not already set)
	if cfg.Username == "" || cfg.Password == "" {
		if user, pass, ok := resolveCredentialsFromEnv(namespace); ok {
			if cfg.Username == "" {
				cfg.Username = user
			}
			if cfg.Password == "" {
				cfg.Password = pass
			}
		}
	}

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
// - "auto" or ""  → runtime.GOOS: windows→"prod", else→"dev"
// - "public" or "PUBLIC" → "" (Nacos public namespace)
// - any other value → used as-is
func resolveNamespace(ns string) string {
	if ns == "" || ns == "auto" {
		if runtime.GOOS == "windows" {
			return "prod"
		}
		return "dev"
	}
	if ns == "public" || ns == "PUBLIC" {
		return ""
	}
	return ns
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
			if err := json.Unmarshal(jsonData, config.Admin); err != nil {
				logger.Error("merge config.admin failed", "error", err)
			}
		}
	}

	// Merge logging config
	if data, ok := getOptionalConfigValue(client, "config.logging", group); ok {
		jsonData, convErr := convertToJSON(data, logger)
		if convErr == nil {
			if config.Logging == nil {
				config.Logging = &caddy.Logging{}
			}
			if err := json.Unmarshal(jsonData, config.Logging); err != nil {
				logger.Error("merge config.logging failed", "error", err)
			}
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
			if err := json.Unmarshal(jsonData, &config.AppsRaw); err != nil {
				logger.Error("merge config.apps failed", "error", err)
			}
		}
	}

	// Merge http server routes
	if config.AppsRaw != nil {
		if httpRaw, hasHTTP := config.AppsRaw["http"]; hasHTTP {
			httpApp := caddyhttp.App{}
			if err := json.Unmarshal(httpRaw, &httpApp); err != nil {
				logger.Error("parse http app config", "error", err)
			} else {
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

					if bytes.Equal(newConfig, lastConfigJSON) {
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
	}


// getVersionDisplay reads the version DATA_ID for logging.
func getVersionDisplay(client clientInterface, group string) string {
	v, err := getConfigValue(client, "version", group)
	if err != nil {
		logger.Debug("read version for display failed", "error", err)
	}
	return v
}
