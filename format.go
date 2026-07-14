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

	// When the content starts with { or [, it could be JSON or a Caddyfile/TOML.
	// Validate as JSON to disambiguate.
	if first == '{' || first == '[' {
		if json.Valid([]byte(trimmed)) {
			return FormatJSON
		}
		// Invalid JSON starting with { → Caddyfile global options block
		if first == '{' {
			return FormatCaddyfile
		}
		// Invalid JSON starting with [ → TOML table header
		return FormatTOML
	}

	lines := strings.SplitN(trimmed, "\n", 10)

	// TOML: first non-empty line starts with [...] or contains key = value
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
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

	// YAML: contains "key: value" pattern in first content line
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
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
		trimmed := strings.TrimSpace(data)
		if trimmed == "" {
			return []byte{}, nil
		}
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

