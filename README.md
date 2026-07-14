# Caddy Nacos Config Adapter

[![Test](https://github.com/Hoverhuang-er/caddy-nacos-adapter/actions/workflows/test.yml/badge.svg)](https://github.com/Hoverhuang-er/caddy-nacos-adapter/actions/workflows/test.yml)
[![Release](https://github.com/Hoverhuang-er/caddy-nacos-adapter/actions/workflows/release.yml/badge.svg)](https://github.com/Hoverhuang-er/caddy-nacos-adapter/actions/workflows/release.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Hoverhuang-er/caddy-nacos-adapter.svg)](https://pkg.go.dev/github.com/Hoverhuang-er/caddy-nacos-adapter)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Hoverhuang-er/caddy-nacos-adapter)](https://golang.org/dl/)

A Caddy [config adapter](https://caddyserver.com/docs/config-adapters) that reads Caddy configuration from [Nacos](https://nacos.io/). Supports hot-reload via Nacos push-based `ListenConfig`, auto-detects config format (JSON/YAML/TOML/Caddyfile), and uses `log/slog` for structured logging.

---

## Features

- **Read Caddy config from Nacos** — no static config files, dynamically load from Nacos at startup
- **Hot-reload** — Nacos push-based `ListenConfig` triggers `caddy.Load()` on config changes, no restart needed
- **Multi-format auto-detection** — JSON, YAML, TOML, and Caddyfile formats supported per DATA_ID
- **Environment-based namespace** — `runtime.GOOS == "windows"` → `"prod"`, others → `"dev"`
- **Granular DATA_IDs** — follows mysql-adapter pattern: version, config, config.admin, config.logging, config.apps, routes
- **No config file mode** — use `GetNacosConfig` var func for hardcoded connection info
- **log/slog** — modern Go structured logging, Caddy captures stderr from plugins

---

## Quick Start

### Prerequisites

- Go 1.26+
- A running Nacos server

### 1. Install

Build Caddy with the nacos adapter using [xcaddy](https://github.com/caddyserver/xcaddy):

```bash
xcaddy build --with github.com/Hoverhuang-er/caddy-nacos-adapter
```

Or download a pre-built binary from [Releases](https://github.com/Hoverhuang-er/caddy-nacos-adapter/releases).

### 2. Set credentials

Set the `CNA` environment variable with base64-encoded Nacos credentials:

```bash
# Format: namespace1:username1:password1;namespace2:username2:password2
export CNA=$(echo -n "dev:admin:nacos;prod:admin:nacos123" | base64)
```

### 3. Prepare nacos.json

Create a `nacos.json` file (passed to Caddy as config):

```json
{
  "serverAddr": "127.0.0.1",
  "serverPort": 8848,
  "dataIds": ["version", "config", "config.admin", "config.apps"],
  "group": "DEFAULT_GROUP",
  "namespace": ""
}
```

### 4. Run

```bash
caddy run --adapter nacos --config ./nacos.json
```

---

## Configuration

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `CNA` | **Yes** | Base64-encoded credentials `ns:user:pass;ns:user:pass` |
| `CADDY_NACOS_SERVER_ADDR` | No | Override Nacos server address |
| `CADDY_NACOS_SERVER_PORT` | No | Override Nacos server port |
| `CADDY_NACOS_NAMESPACE` | No | Override namespace |
| `CADDY_NACOS_GROUP` | No | Override group |
| `CADDY_NACOS_DATA_IDS` | No | Override comma-separated DATA_IDs |

### nacos.json Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `serverAddr` | string | `"127.0.0.1"` | Nacos server address |
| `serverPort` | uint64 | `8848` | Nacos server port |
| `username` | string | from `CNA` or inline | Nacos username |
| `password` | string | from `CNA` or inline | Nacos password |
| `namespace` | string | auto | `"auto"`/`""` → win: `prod`, else `dev`; `"public"`/`"PUBLIC"` → `""` |
| `dataIds` | []string | — | List of DATA_IDs to load and watch |
| `group` | string | `"DEFAULT_GROUP"` | Nacos config group |

### Multi-Tenant: Multiple Nacos Sources

For deployments with multiple Nacos namespaces or servers, pass an array of configurations:

```json
[
  {
    "serverAddr": "127.0.0.1",
    "serverPort": 8848,
    "namespace": "dev",
    "username": "admin",
    "password": "nacos",
    "dataIds": ["config", "config.admin"],
    "group": "DEV_GROUP"
  },
  {
    "serverAddr": "10.0.0.100",
    "serverPort": 8848,
    "namespace": "prod",
    "username": "admin",
    "password": "nacos123",
    "dataIds": ["config", "config.apps"],
    "group": "PROD_GROUP"
  }
]
```

Each source can point to a different Nacos server, namespace, or group. Configs from all sources are merged — later sources override earlier ones for the same keys (e.g., `admin`, `logging`, `apps`). Apps from different sources are merged at the top level.

Credentials can be provided inline (as shown above) or via the `CNA` environment variable. If inline credentials are present for any source, the `CNA` env var becomes optional.

### Namespace Resolution

| Input | GOOS=windows | GOOS=darwin/linux |
|-------|:---:|:---:|
| `""` or `"auto"` | `"prod"` | `"dev"` |
| `"public"` / `"PUBLIC"` | `""` (public) | `""` (public) |
| Any other string | as-is | as-is |

### DATA_ID Layout

| DATA_ID | Content | Merge behavior |
|---------|---------|----------------|
| `version` | any string | Display only; change triggers hot-reload |
| `config` | base Caddy JSON | Unmarshalled into `caddy.Config` |
| `config.admin` | admin config | Merged into `config.Admin` |
| `config.logging` | logging config | Merged into `config.Logging` |
| `config.storage` | storage config | Merged into `config.StorageRaw` |
| `config.apps` | apps config | Merged into `config.AppsRaw` |
| `config.apps.http.servers.*.routes` | routes | Appended to the named server's route list |

Each DATA_ID supports auto-detected format: JSON, YAML, TOML, or Caddyfile.

---

## Hot-Reload

The adapter registers Nacos push listeners on all DATA_IDs. When Nacos detects a config change, it pushes the new value, the adapter rebuilds the Caddy config, and calls `caddy.Load()` — no restart, no downtime.

The reload is protected by a mutex to prevent concurrent reloads from overlapping callbacks.

---

## Advanced: Hardcoded Config (No nacos.json)

For automated deployments where config file management is undesirable, set `GetNacosConfig` in an `init()` function:

```go
package main

import (
    "github.com/caddyserver/caddy/v2"
    _ "github.com/Hoverhuang-er/caddy-nacos-adapter"
    "github.com/Hoverhuang-er/caddy-nacos-adapter"
)

func init() {
    caddynacos.GetNacosConfig = func() *caddynacos.NacosConfig {
        return &caddynacos.NacosConfig{
            ServerAddr: "10.0.0.100",
            ServerPort: 8848,
            Username:   "admin",
            Password:   "nacos",
            DataIDs:    []string{"version", "config"},
            Group:      "CADDY_GROUP",
        }
    }
}
```

Then build and run without any config file:

```bash
xcaddy build --with github.com/Hoverhuang-er/caddy-nacos-adapter
./caddy run --adapter nacos
```

---

## Downloads

Pre-built Caddy binaries with the nacos adapter are available for each [release](https://github.com/Hoverhuang-er/caddy-nacos-adapter/releases):

| Platform | Architecture | Format |
|----------|:-----------:|:------:|
| Linux | amd64 | `.tar.gz` |
| Linux | arm64 | `.tar.gz` |
| macOS | amd64 | `.tar.gz` |
| macOS | arm64 | `.tar.gz` |
| Windows | amd64 | `.zip` |
| Windows | arm64 | `.zip` |

You can also build from source with a single command:

```bash
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
xcaddy build --with github.com/Hoverhuang-er/caddy-nacos-adapter
```

---

## Development

### Prerequisites

- Go 1.26+
- [xcaddy](https://github.com/caddyserver/xcaddy)

### Build locally

```bash
cd ~/workspace/hxgm/caddynacos

# Test
go test -v -count=1 ./...

# Build with xcaddy
xcaddy build --with github.com/Hoverhuang-er/caddy-nacos-adapter=.
```

### Run tests

```bash
go test -v -count=1 ./...
```

---

## GitHub Actions

| Workflow | Trigger | Description |
|----------|---------|-------------|
| [Test](.github/workflows/test.yml) | Push/PR to `main` | Runs `go build` + `go test` on Linux, macOS, Windows |
| [Release](.github/workflows/release.yml) | Tag `v*.*.*` | Builds all platform binaries via GoReleaser + xcaddy and creates a GitHub Release |

### Making a Release

```bash
git tag v0.0.1
git push origin v0.0.1
```

The release workflow builds binaries for all 6 platforms and creates a GitHub Release with changelog.

---

## Architecture

```
┌─────────────────────┐     ┌──────────────────────┐     ┌──────────────┐
│  Caddy (xcaddy)     │────▶│  nacos adapter        │────▶│  Nacos       │
│  with nacos adapter │◀────│  (hot-reload loop)    │◀────│  (push)      │
└─────────────────────┘     └──────────────────────┘     └──────────────┘
```

The adapter registers as `"nacos"` with Caddy's config adapter system. On startup, `Adapt()` is called with the contents of the config file (nacos.json) which contains Nacos connection parameters. The adapter:

1. Connects to Nacos
2. Reads all specified DATA_IDs
3. Auto-detects and converts each to JSON
4. Assembles the full Caddy JSON config
5. Registers push listeners for hot-reload

---

## Compatibility

- **Caddy**: v2.9.1+
- **Nacos SDK**: v2.2.7+
- **Go**: 1.26+

---

## License

MIT
