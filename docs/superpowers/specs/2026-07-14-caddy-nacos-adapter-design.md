# Caddy Nacos Config Adapter — Design Spec

## Overview

A Caddy [config adapter](https://caddyserver.com/docs/config-adapters) that reads Caddy configuration from Nacos, supports hot-reload via Nacos push-based `ListenConfig`, auto-detects config format (JSON/YAML/TOML/Caddyfile), and uses `log/slog` for logging.

## Motivation

Replace static Caddy config files with dynamic Nacos-pushed configuration. The adapter bridges Nacos to Caddy's config loading system, enabling:

- Runtime namespace selection based on `runtime.GOOS` (Windows → `prod`, others → `dev`)
- Hot-reload without restart when Nacos config changes
- Multi-format configuration storage in Nacos (YAML/JSON/TOML/Caddyfile)

## Architecture

### Module Layout

```
caddy-nacos-adapter/
├── nacos.go          # Adapter registration, Adapt() entry, hot-reload loop
├── format.go         # Format detection and conversion (JSON/YAML/TOML/Caddyfile)
├── go.mod
└── go.sum
```

### Package: `caddynacos`

Registered adapter name: `"nacos"`

### Key Interfaces

```go
// var func pattern — users override this in their main.go init()
var GetNacosConfig func() *NacosConfig

type NacosConfig struct {
    ServerAddr string   // Nacos server address
    ServerPort uint64   // Nacos server port
    Username   string   // Nacos auth username
    Password   string   // Nacos auth password
    DataIDs    []string // List of DATA_IDs to monitor
    Group      string   // Nacos config group, default "DEFAULT_GROUP"
}

// AdapterConfig — deserialized from nacos.json
type AdapterConfig struct {
    ServerAddr string   `json:"serverAddr"`
    ServerPort uint64   `json:"serverPort"`
    Username   string   `json:"username,omitempty"`
    Password   string   `json:"password,omitempty"`
    Namespace  string   `json:"namespace,omitempty"`
    DataIDs    []string `json:"dataIds"`
    Group      string   `json:"group,omitempty"`
}

type Adapter struct{}

func (a Adapter) Adapt(body []byte, options map[string]any) ([]byte, []caddyconfig.Warning, error)
```

### Namespace Resolution

```go
func resolveNamespace(override string) string {
    if override != "" {
        return override          // nacos.json 中的 namespace 优先
    }
    if runtime.GOOS == "windows" {
        return "prod"
    }
    return "dev"
}
```

## Configuration Sources (priority order)

1. **`GetNacosConfig` var func** — if non-nil, provides hardcoded NacosConfig
2. **`nacos.json`** — parsed from `body` in `Adapt()`, overrides empty fields from var func
3. **Defaults** — Group defaults to `"DEFAULT_GROUP"`, ServerPort defaults to `8848`

## Format Auto-Detection

Each DATA_ID content is independently format-detected and converted to JSON:

| Content pattern | Format | Strategy |
|---|---|---|
| Starts with `{` or `[` | JSON | `json.Unmarshal` → keep as-is |
| Starts with TOML table `[...]` or `key = value` | TOML | `BurntSushi/toml.Unmarshal` → `json.Marshal` |
| Has `key: value` or YAML structure | YAML | `gopkg.in/yaml.v3.Unmarshal` → `json.Marshal` |
| Everything else | Caddyfile | `caddyconfig.GetAdapter("caddyfile").Adapt(...)` |

Dependencies for format conversion:
- `gopkg.in/yaml.v3` — YAML
- `github.com/BurntSushi/toml` — TOML

Both are optional (lazy-loaded on first use of the respective format).

## Config Assembly Logic

Follows the mysql-adapter key pattern:

1. **DATA_ID `"version"`** — version string, change triggers reload (tracked internally)
2. **DATA_ID `"config"`** — base Caddy config (any format); unmarshal into `caddy.Config{}`
3. **DATA_ID `"config.admin"`** — merge into `config.Admin`
4. **DATA_ID `"config.logging"`** — merge into `config.Logging`
5. **DATA_ID `"config.storage"`** — merge into `config.StorageRaw`
6. **DATA_ID `"config.apps"`** — merge into `config.AppsRaw`
7. **DATA_ID `"config.apps.http.servers.<name>.routes"`** — append routes to the named server

Each DATA_ID content is independently format-detected and converted to JSON before assembly.

## Hot-Reload (Nacos ListenConfig)

Unlike the mysql adapter's polling approach, Nacos SDK v2 provides push-based listening:

```go
func startListeners(client naming_client.IConfigClient, cfg *AdapterConfig, logger *slog.Logger) {
    for _, dataID := range cfg.DataIDs {
        go func(did string) {
            err := client.ListenConfig(vo.ConfigParam{
                DataId: did,
                Group:  cfg.Group,
                OnChange: func(namespace, group, dataId, data string) {
                    logger.Info("nacos config changed", "dataId", dataId, "group", group)
                    newConfig, err := buildConfig(client, cfg.DataIDs, cfg.Group, logger)
                    if err != nil {
                        logger.Error("rebuild config failed", "error", err)
                        return
                    }
                    caddy.Load(newConfig, false)
                },
            })
            if err != nil {
                logger.Error("listen nacos config failed", "dataId", did, "error", err)
            }
        }(dataID)
    }
}
```

Key differences from mysql-adapter polling:
- No polling interval — Nacos SDK manages long-polling internally
- No `version` field needed for trigger — OnChange fires on any content change
- `caddy.Load()` called directly in callback, guarded by sync.Once/sync.Mutex to prevent concurrent reloads

## Logging

```go
var logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
    Level: slog.LevelInfo,
}))
```

- Uses `log/slog` standard library
- Writes to stderr (Caddy captures its own log output including stderr from plugins)
- Log levels: Info (reload events), Warn (format fallbacks), Error (Nacos connection failures)

## Build & Usage

### With xcaddy (standard adapter mode)

```bash
# Build
xcaddy build --with github.com/<user>/caddy-nacos-adapter

# Run with nacos config
caddy run --adapter nacos --config ./nacos.json
```

### nacos.json format

```json
{
  "serverAddr": "127.0.0.1",
  "serverPort": 8848,
  "username": "nacos",
  "password": "nacos",
  "dataIds": ["version", "config", "config.admin", "config.apps"],
  "group": "DEFAULT_GROUP",
  "namespace": ""
}
```

### Custom main.go with hardcoded config

```go
package main

import (
    "github.com/caddyserver/caddy/v2"
    _ "github.com/<user>/caddy-nacos-adapter/caddynacos"
)

func init() {
    caddynacos.GetNacosConfig = func() *caddynacos.NacosConfig {
        return &caddynacos.NacosConfig{
            ServerAddr: "10.0.0.100",
            ServerPort: 8848,
            DataIDs:    []string{"version", "config"},
            Group:      "CADDY_GROUP",
        }
    }
}
```

## Error Handling

| Scenario | Behavior |
|---|---|
| Nacos connection failure | `Adapt()` returns error → Caddy startup fails |
| Nacos config parse error | `Adapt()` returns error → Caddy startup fails |
| Nacos listener setup failure | Logged as error, process continues with last good config |
| Hot-reload config parse failure | Logged as error, existing config preserved |
| Format detection failure per DATA_ID | Warn and skip that DATA_ID |

## Design Decisions

1. **Push over Poll**: Nacos ListenConfig provides real-time push. No polling interval configuration needed.

2. **Multiple DATA_IDs over single**: Follows mysql-adapter pattern for granular control. Each section can be independently updated in Nacos.

3. **Format auto-detection**: Minimizes Nacos-side configuration. Content format is inferred, not declared.

4. **var func + JSON file**: Maximum flexibility. Hardcoded config for automated deployments, JSON file for manual setups.

5. **log/slog over caddy.Log()**: Modern Go standard library. Caddy captures stderr from plugins, so this works naturally.

## Future Considerations (NOT in scope for v1)

- Schema validation of Nacos content against Caddy JSON schema
- Encrypted transport (Nacos TLS)
- Multi-tenancy (multiple Nacos namespaces via multiple adapter instances)
- Prometheus metrics for reload events

---

*Spec v1 — 2026-07-14*
