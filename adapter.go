package caddynacos

import (
	"bytes"
	"encoding/base64"
	"encoding/json" // 保留用于 json.RawMessage（Caddy 类型兼容）
	jsonv2 "encoding/json/v2"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// 格式检测与转换
// ---------------------------------------------------------------------------

// ConfigFormat 表示检测到的配置格式类型。
type ConfigFormat int

const (
	FormatJSON      ConfigFormat = iota // JSON 格式
	FormatYAML                          // YAML 格式
	FormatTOML                          // TOML 格式
	FormatCaddyfile                     // Caddyfile 格式
)

// detectFormat 从原始内容字符串中检测配置格式。
func detectFormat(data string) ConfigFormat {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return FormatJSON
	}

	first := trimmed[0]

	// 当内容以 { 或 [ 开头时，可能是 JSON、Caddyfile 或 TOML。
	// 先尝试用 JSON 解析来区分。
	if first == '{' || first == '[' {
		var v any
		if err := jsonv2.Unmarshal([]byte(trimmed), &v); err == nil {
			return FormatJSON
		}
		if first == '{' {
			return FormatCaddyfile
		}
		return FormatTOML
	}

	lines := strings.SplitN(trimmed, "\n", 10)

	// TOML: 首个非空行以 [...] 开头或包含 key = value
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" ||
			strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			return FormatTOML
		}
		if strings.Contains(line, " = ") {
			return FormatTOML
		}
		break
	}

	// YAML: 首个内容行包含 "key: value" 模式
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" ||
			strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "//") {
			continue
		}
		if strings.Contains(line, ": ") || strings.HasSuffix(line, ":") {
			return FormatYAML
		}
		break
	}

	return FormatCaddyfile
}

// convertToJSON 将任意支持格式的配置内容转换为 Caddy JSON 字节。
func convertToJSON(data string, logger *slog.Logger) ([]byte, error) {
	format := detectFormat(data)

	switch format {
	case FormatJSON:
		trimmed := strings.TrimSpace(data)
		if trimmed == "" {
			return []byte{}, nil
		}
		var raw json.RawMessage
		if err := jsonv2.Unmarshal([]byte(data), &raw); err != nil {
			return nil, fmt.Errorf("无效 JSON: %w", err)
		}
		return raw, nil

	case FormatYAML:
		var parsed any
		if err := yaml.Unmarshal([]byte(data), &parsed); err != nil {
			return nil, fmt.Errorf("无效 YAML: %w", err)
		}
		parsed = yamlToJSONSafe(parsed)
		result, err := jsonv2.Marshal(parsed)
		if err != nil {
			return nil, fmt.Errorf("YAML 转 JSON 序列化失败: %w", err)
		}
		return result, nil

	case FormatTOML:
		var parsed map[string]any
		if _, err := toml.Decode(data, &parsed); err != nil {
			return nil, fmt.Errorf("无效 TOML: %w", err)
		}
		result, err := jsonv2.Marshal(parsed)
		if err != nil {
			return nil, fmt.Errorf("TOML 转 JSON 序列化失败: %w", err)
		}
		return result, nil

	case FormatCaddyfile:
		adapter := caddyconfig.GetAdapter("caddyfile")
		if adapter == nil {
			return nil, fmt.Errorf("Caddyfile 适配器不可用")
		}
		result, warnings, err := adapter.Adapt([]byte(data), nil)
		if err != nil {
			return nil, fmt.Errorf("Caddyfile 适配失败: %w", err)
		}
		for _, w := range warnings {
			logger.Warn("Caddyfile 适配器警告", "msg", w.Message)
		}
		return result, nil

	default:
		return nil, fmt.Errorf("未知的配置格式")
	}
}

// yamlToJSONSafe 将 YAML 原生类型（map[any]any）转换为 JSON 安全类型
//（map[string]any）。
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

// ---------------------------------------------------------------------------
// 配置类型与适配器入口
// ---------------------------------------------------------------------------

// GetNacosConfig 是 var func 模式，用户可在 init() 中覆写此函数
// 来硬编码 Nacos 连接信息，无需配置文件。
var GetNacosConfig func() *NacosConfig

// NacosConfig 包含用于 var func 模式的硬编码 Nacos 连接参数。
type NacosConfig struct {
	ServerAddr string
	ServerPort uint64
	Username   string
	Password   string
	DataIDs    []string
	Group      string
}

// AdapterConfig 存储单个 Nacos 源的配置项。
type AdapterConfig struct {
	ServerAddr string   `json:"serverAddr"`
	ServerPort uint64   `json:"serverPort"`
	Username   string   `json:"username,omitempty"`
	Password   string   `json:"password,omitempty"`
	Namespace  string   `json:"namespace,omitempty"`
	DataIDs    []string `json:"dataIds"`
	Group      string   `json:"group,omitempty"`
}

// nacosSource 连接一个 Nacos 客户端及其配置，用于多源管理。
type nacosSource struct {
	client clientInterface
	cfg    *AdapterConfig
}

// parseConfigs 解析 body 为单个或数组形式的 Nacos 源配置列表。
// body 可以是单个 AdapterConfig 对象或 AdapterConfig 数组。
// 当 body 为空时，若 GetNacosConfig 已设置则生成单元素列表。
func parseConfigs(body []byte) ([]*AdapterConfig, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		// 空 body，回退到 GetNacosConfig var func 或默认配置
		if GetNacosConfig != nil {
			nc := GetNacosConfig()
			if nc != nil {
				return []*AdapterConfig{{
					ServerAddr: nc.ServerAddr,
					ServerPort: nc.ServerPort,
					Username:   nc.Username,
					Password:   nc.Password,
					DataIDs:    nc.DataIDs,
					Group:      nc.Group,
				}}, nil
			}
		}
		return []*AdapterConfig{{
			ServerAddr: "127.0.0.1",
			ServerPort: 8848,
			Group:      "DEFAULT_GROUP",
			DataIDs:    []string{"version", "config"},
		}}, nil
	}

	// 尝试解析为数组
	var cfgs []*AdapterConfig
	if err := jsonv2.Unmarshal(body, &cfgs); err == nil {
		return cfgs, nil
	}

	// 尝试解析为单个对象
	var cfg AdapterConfig
	if err := jsonv2.Unmarshal(body, &cfg); err == nil {
		if len(cfg.DataIDs) > 0 {
			return []*AdapterConfig{&cfg}, nil
		}
	}

	return nil, fmt.Errorf("无法解析 nacos.json: 期望单个配置对象或配置数组")
}

// fillFromNacosConfig 用 var func 提供的值覆盖配置中的空字段。
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

// logger 是包级别的 slog 日志器，输出到 stderr。
var logger = slog.New(slog.NewTextHandler(
	os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

func init() {
	caddyconfig.RegisterAdapter("nacos", Adapter{})
}

// EnvNacosAuth 是存放 base64 编码 Nacos 凭据的环境变量名。
// 解码后格式: ns1:username1:password1;ns2:username2:password2
const EnvNacosAuth = "CNA"

// resolveCredentialsFromEnv 解码并解析 CNA 环境变量，
// 返回与指定 namespace 匹配的用户名和密码。
func resolveCredentialsFromEnv(
	namespace string,
) (username, password string, ok bool) {
	encoded := os.Getenv(EnvNacosAuth)
	if encoded == "" {
		return "", "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		logger.Warn("解码 CNA 环境变量失败", "error", err)
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
			logger.Warn("CNA 环境变量中存在无效的凭据条目", "entry", pair)
			continue
		}
		ns, user, pass := parts[0], parts[1], parts[2]
		if ns == namespace {
			return user, pass, true
		}
	}

	return "", "", false
}

// Adapter 实现了 caddyconfig.Adapter 接口，用于 Nacos 配置适配。
type Adapter struct{}

var _ caddyconfig.Adapter = (*Adapter)(nil)

// Adapt 从 Nacos 读取配置并返回 Caddy JSON。
// body 可以是单个 AdapterConfig 对象或 AdapterConfig 数组，支持多 Nacos 源。
// 必须在环境中设置 CNA 环境变量，或在配置中直接内联凭据。
func (a Adapter) Adapt(body []byte, options map[string]any) (
	[]byte, []caddyconfig.Warning, error,
) {
	// 解析配置（单对象或数组）
	configs, err := parseConfigs(body)
	if err != nil {
		return nil, nil, err
	}

	if len(configs) == 0 {
		return nil, nil, fmt.Errorf("Nacos 配置列表为空，请检查 nacos.json")
	}

	// 是否有内联凭据（直接在配置中写了 username/password）？
	hasInlineCreds := false
	for _, cfg := range configs {
		if cfg.Username != "" && cfg.Password != "" {
			hasInlineCreds = true
			break
		}
	}

	// 仅当没有任何内联凭据时才需要 CNA 环境变量
	if !hasInlineCreds && os.Getenv(EnvNacosAuth) == "" {
		return nil, nil, fmt.Errorf(
			"CNA 环境变量或配置内联凭据必填: " +
				"设置 base64 编码的凭据 (ns:user:pass;ns:user:pass)")
	}

	// 应用 var func 覆写（仅对未内联凭据且未指定 serverAddr 的条目生效）
	varGetNacosConfig := (GetNacosConfig != nil && GetNacosConfig() != nil)

	// 构建每个源的 Nacos 客户端
	var sources []nacosSource
	for i, cfg := range configs {
		// 应用 var func 默认值
		if varGetNacosConfig {
			nc := GetNacosConfig()
			cfg.fillFromNacosConfig(nc)
		}

		// 解析 namespace
		namespace := resolveNamespace(cfg.Namespace)

		// 从 CNA 环境变量中解析凭据（仅当未内联时）
		if cfg.Username == "" || cfg.Password == "" {
			if user, pass, ok := resolveCredentialsFromEnv(namespace); ok {
				cfg.Username = user
				cfg.Password = pass
			}
		}

		if cfg.Username == "" || cfg.Password == "" {
			return nil, nil, fmt.Errorf(
				"源 #%d (namespace=%q): 未找到凭据，请检查 CNA 环境变量或配置内联",
				i+1, namespace)
		}

		if cfg.ServerAddr == "" {
			cfg.ServerAddr = "127.0.0.1"
		}
		if cfg.ServerPort == 0 {
			cfg.ServerPort = 8848
		}
		if len(cfg.DataIDs) == 0 {
			return nil, nil, fmt.Errorf(
				"源 #%d (namespace=%q): dataIds 不能为空",
				i+1, namespace)
		}
		if cfg.Group == "" {
			cfg.Group = "DEFAULT_GROUP"
		}

		logger.Info("Nacos 适配器源",
			"source", i+1,
			"server", net.JoinHostPort(
				cfg.ServerAddr, strconv.Itoa(int(cfg.ServerPort))),
			"namespace", namespace,
			"dataIds", cfg.DataIDs,
			"group", cfg.Group,
		)

		// 创建 Nacos 配置客户端
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
			return nil, nil, fmt.Errorf(
				"创建 Nacos 客户端失败 (源 #%d): %w", i+1, err)
		}

		sources = append(sources, nacosSource{client: client, cfg: cfg})
	}

	// 构建并合并所有源的配置
	configJSON, err := buildAndMergeConfigs(sources)
	if err != nil {
		return nil, nil, fmt.Errorf("从 Nacos 构建配置失败: %w", err)
	}

	// 启动热加载监听器
	startListeners(sources, configJSON)

	return configJSON, nil, nil
}

// resolveNamespace 根据 runtime.GOOS 确定 Nacos namespace。
// - "auto" 或 ""  → runtime.GOOS: windows→"prod", 其他→"dev"
// - "public" 或 "PUBLIC" → ""（Nacos 公共 namespace）
// - 其他值 → 直接使用
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

// buildConfig 从 Nacos 读取所有 DATA_ID 并组装成 Caddy JSON 配置。
func buildConfig(
	client clientInterface, dataIDs []string, group string,
) ([]byte, error) {
	// 读取版本号（用于日志记录/追踪）
	version, _ := getConfigValue(client, "version", group)
	if version != "" {
		logger.Info("Nacos 配置版本", "version", version)
	}

	// 构建基础配置
	configData, err := getConfigValue(client, "config", group)
	if err != nil {
		return nil, fmt.Errorf("从 Nacos 读取 'config' 失败: %w", err)
	}

	config := caddy.Config{}
	if configData != "" {
		configJSON, convErr := convertToJSON(configData, logger)
		if convErr != nil {
			return nil, fmt.Errorf("转换 'config' 为 JSON 失败: %w", convErr)
		}
		if err := jsonv2.Unmarshal(configJSON, &config); err != nil {
			return nil, fmt.Errorf(
				"反序列化 'config' 到 caddy.Config 失败: %w", err)
		}
	}

	// 合并 admin 配置
	if data, ok := getOptionalConfigValue(
		client, "config.admin", group); ok {
		jsonData, convErr := convertToJSON(data, logger)
		if convErr == nil {
			if config.Admin == nil {
				config.Admin = &caddy.AdminConfig{}
			}
			if err := jsonv2.Unmarshal(
				jsonData, config.Admin); err != nil {
				logger.Error("合并 config.admin 失败", "error", err)
			}
		}
	}

	// 合并 logging 配置
	if data, ok := getOptionalConfigValue(
		client, "config.logging", group); ok {
		jsonData, convErr := convertToJSON(data, logger)
		if convErr == nil {
			if config.Logging == nil {
				config.Logging = &caddy.Logging{}
			}
			if err := jsonv2.Unmarshal(
				jsonData, config.Logging); err != nil {
				logger.Error("合并 config.logging 失败", "error", err)
			}
		}
	}

	// 合并 storage 配置
	if data, ok := getOptionalConfigValue(
		client, "config.storage", group); ok {
		jsonData, convErr := convertToJSON(data, logger)
		if convErr == nil {
			config.StorageRaw = json.RawMessage(jsonData)
		}
	}

	// 合并 apps 配置
	if data, ok := getOptionalConfigValue(
		client, "config.apps", group); ok {
		jsonData, convErr := convertToJSON(data, logger)
		if convErr == nil {
			config.AppsRaw = caddy.ModuleMap{}
			if err := jsonv2.Unmarshal(
				jsonData, &config.AppsRaw); err != nil {
				logger.Error("合并 config.apps 失败", "error", err)
			}
		}
	}

	// 合并 HTTP 服务器路由
	if config.AppsRaw != nil {
		if httpRaw, hasHTTP := config.AppsRaw["http"]; hasHTTP {
			httpApp := caddyhttp.App{}
			if err := jsonv2.Unmarshal(httpRaw, &httpApp); err != nil {
				logger.Error("解析 HTTP 应用配置失败", "error", err)
			} else {
				changed := false
				if httpApp.Servers != nil {
					for serverKey := range httpApp.Servers {
						routesKey := "config.apps.http.servers." +
							serverKey + ".routes"
						values, _ := getOptionalConfigValues(
							client, routesKey, group)
						if len(values) > 0 {
							if httpApp.Servers[serverKey].Routes == nil {
								httpApp.Servers[serverKey].Routes =
									make([]caddyhttp.Route, 0)
							}
							for _, routeData := range values {
								jsonData, convErr := convertToJSON(
									routeData, logger)
								if convErr != nil {
									logger.Error(
										"转换路由为 JSON 失败",
										"error", convErr,
										"dataId", routesKey)
									continue
								}
								var route caddyhttp.Route
								if err := jsonv2.Unmarshal(
									jsonData, &route); err == nil {
									httpApp.Servers[serverKey].Routes =
										append(
											httpApp.Servers[serverKey].Routes,
											route,
										)
									changed = true
								}
							}
						}
					}
				}
				if changed {
					var warnings []caddyconfig.Warning
					config.AppsRaw["http"] = caddyconfig.JSON(
						httpApp, &warnings)
					for _, w := range warnings {
						logger.Warn("重新编码 HTTP 应用", "msg", w.Message)
					}
				}
			}
		}
	}

	return jsonv2.Marshal(config)
}

// buildAndMergeConfigs 遍历所有 Nacos 源，构建并合并配置。
// 后面的源会覆盖前面源的相同键。
func buildAndMergeConfigs(sources []nacosSource) ([]byte, error) {
	var merged *caddy.Config

	for _, src := range sources {
		configJSON, err := buildConfig(src.client, src.cfg.DataIDs, src.cfg.Group)
		if err != nil {
			logger.Error("从 Nacos 构建配置失败",
				"namespace", src.cfg.Namespace,
				"error", err)
			continue
		}

		if merged == nil {
			var cfg caddy.Config
			if err := jsonv2.Unmarshal(configJSON, &cfg); err != nil {
				logger.Error("反序列化配置失败", "error", err)
				continue
			}
			merged = &cfg
			continue
		}

		// 合并：将新配置合并到已存在的 merged 中
		var partial caddy.Config
		if err := jsonv2.Unmarshal(configJSON, &partial); err != nil {
			logger.Error("反序列化部分配置失败", "error", err)
			continue
		}

		// 合并 Admin
		if partial.Admin != nil {
			if merged.Admin == nil {
				merged.Admin = &caddy.AdminConfig{}
			}
			patch, _ := jsonv2.Marshal(partial.Admin)
			jsonv2.Unmarshal(patch, merged.Admin)
		}

		// 合并 Logging
		if partial.Logging != nil {
			if merged.Logging == nil {
				merged.Logging = &caddy.Logging{}
			}
			patch, _ := jsonv2.Marshal(partial.Logging)
			jsonv2.Unmarshal(patch, merged.Logging)
		}

		// 合并 StorageRaw
		if partial.StorageRaw != nil {
			merged.StorageRaw = partial.StorageRaw
		}

		// 合并 AppsRaw
		if partial.AppsRaw != nil {
			if merged.AppsRaw == nil {
				merged.AppsRaw = caddy.ModuleMap{}
			}
			for k, v := range partial.AppsRaw {
				merged.AppsRaw[k] = v
			}
		}
	}

	if merged == nil {
		return jsonv2.Marshal(caddy.Config{})
	}

	return jsonv2.Marshal(merged)
}

// clientInterface 抽象了 Nacos 配置客户端，便于测试时使用 mock。
type clientInterface interface {
	GetConfig(param vo.ConfigParam) (string, error)
	ListenConfig(param vo.ConfigParam) error
	CancelListenConfig(param vo.ConfigParam) error
}

// getConfigValue 从 Nacos 读取单个配置值。
func getConfigValue(
	client clientInterface, dataID, group string,
) (string, error) {
	return client.GetConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  group,
	})
}

// getOptionalConfigValue 读取配置值，未找到时返回 false。
func getOptionalConfigValue(
	client clientInterface, dataID, group string,
) (string, bool) {
	val, err := client.GetConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  group,
	})
	if err != nil || val == "" {
		return "", false
	}
	return val, true
}

// getOptionalConfigValues 返回某个 dataID 的所有匹配配置值。
// 注意: Nacos SDK 每个 dataID 只返回一个值。类似 mysql adapter 的
// "ORDER BY CREATED DESC" 多值匹配模式在 Nacos 中不支持。
// 返回单元素 slice 是为了接口兼容性。
func getOptionalConfigValues(
	client clientInterface, dataID, group string,
) ([]string, bool) {
	val, ok := getOptionalConfigValue(client, dataID, group)
	if !ok {
		return nil, false
	}
	return []string{val}, true
}

// mu 保护来自多个 Nacos 回调的并发重载。
var reloadMu sync.Mutex

// lastConfigJSON 缓存上次成功加载的配置，避免冗余重载。
var lastConfigJSON []byte

// startListeners 在所有源的所有 DATA_ID 上注册 Nacos 推送监听器。
// 当任一源发生配置变更时，从全部源重新构建并合并配置。
func startListeners(
	sources []nacosSource,
	initialConfig []byte,
) {
	lastConfigJSON = initialConfig

	for idx, src := range sources {
		client := src.client
		cfg := src.cfg
		sourceIdx := idx

		for _, dataID := range cfg.DataIDs {
			did := dataID // 在闭包中捕获
			go func() {
				err := client.ListenConfig(vo.ConfigParam{
					DataId: did,
					Group:  cfg.Group,
					OnChange: func(
						namespace, group, dataId, data string,
					) {
						logger.Info("Nacos 配置已变更",
							"source", sourceIdx,
							"dataId", dataId,
							"group", group,
							"namespace", namespace,
						)

						reloadMu.Lock()
						defer reloadMu.Unlock()

						// 从所有源重新构建并合并
						newConfig, err := buildAndMergeConfigs(sources)
						if err != nil {
							logger.Error(
								"从 Nacos 重新构建配置失败",
								"error", err)
							return
						}

						if bytes.Equal(newConfig, lastConfigJSON) {
							logger.Debug("配置未变化，跳过重载")
							return
						}

						if err := caddy.Load(newConfig, false); err != nil {
							logger.Error(
								"caddy.Load 失败",
								"error", err)
							return
						}

						lastConfigJSON = newConfig
						logger.Info("Caddy 配置已从 Nacos 热重载",
							"source", sourceIdx,
							"dataId", dataId,
						)
					},
				})
				if err != nil {
					logger.Error(
						"监听 Nacos 配置失败",
						"source", sourceIdx,
						"dataId", did, "error", err)
				}
			}()
		}
	}
}

// getVersionDisplay 读取版本 DATA_ID 用于日志显示。
func getVersionDisplay(client clientInterface, group string) string {
	v, err := getConfigValue(client, "version", group)
	if err != nil {
		logger.Debug("读取版本号显示失败", "error", err)
	}
	return v
}
