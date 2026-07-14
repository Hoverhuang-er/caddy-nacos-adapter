package caddynacos

import (
	"encoding/json"
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
