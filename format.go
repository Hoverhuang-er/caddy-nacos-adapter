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
		if json.Valid([]byte(trimmed)) {
			return FormatJSON
		}
		// 以 { 开头的无效 JSON → Caddyfile 全局选项块
		if first == '{' {
			return FormatCaddyfile
		}
		// 以 [ 开头的无效 JSON → TOML 表头
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

	// 其他情况均视为 Caddyfile
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
		// 验证为合法 JSON
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			return nil, fmt.Errorf("无效 JSON: %w", err)
		}
		return raw, nil

	case FormatYAML:
		var parsed any
		if err := yaml.Unmarshal([]byte(data), &parsed); err != nil {
			return nil, fmt.Errorf("无效 YAML: %w", err)
		}
		// 将 YAML 原生类型转换为 JSON 安全类型
		parsed = yamlToJSONSafe(parsed)
		result, err := json.Marshal(parsed)
		if err != nil {
			return nil, fmt.Errorf("YAML 转 JSON 序列化失败: %w", err)
		}
		return result, nil

	case FormatTOML:
		var parsed map[string]any
		if _, err := toml.Decode(data, &parsed); err != nil {
			return nil, fmt.Errorf("无效 TOML: %w", err)
		}
		result, err := json.Marshal(parsed)
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
