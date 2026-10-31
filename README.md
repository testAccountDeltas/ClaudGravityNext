# ClaudGravityNext

**Use Claude Code (and any Anthropic-API client) with Gemini models — through a small gateway you run yourself.**

ClaudGravityNext speaks the Anthropic Messages API on the front and Gemini on the back.
Point your client at it, and tools, thinking, images, PDFs and streaming keep working. It is
a single Go binary with a built-in web dashboard. No database to set up, no Docker required.

```
Claude Code ──▶  ClaudGravityNext  ──▶  Gemini
 (Anthropic API)   translate · pool · retry
```

## Why you might want it

- **Drop-in for Claude Code.** `ANTHROPIC_BASE_URL` plus a key, nothing else to patch.
- **Many accounts, one endpoint.** Requests are spread across your account pool, with
  quota-aware cooldowns and automatic failover when one account runs dry.
- **Tool use that holds up.** Tool schemas are translated, malformed arguments repaired,
  long MCP tool names shortened and restored.
- **Cache-friendly.** The request prefix is kept deterministic so prompt caching works, and
  `max_tokens: 0` pre-warms it.
- **Effort levels.** `low`, `medium`, `high`, `xhigh`, `max` map to thinking budgets.
- **Wire-compatible.** SSE grammar, usage numbers, stop reasons and error types follow the
  Anthropic API, so clients don't need special cases.
- **Dashboard.** Add accounts, create API keys, watch quotas and logs in the browser.
- **Safe defaults.** Secrets are generated on first run, SSRF-guarded URL downloads, upstream
  error details are sanitized before they reach clients.

## Quick start

You need [Go](https://go.dev/dl/) (version in `go.mod`) and a Google account to log in with.

**1. Build**

```
go build -o gateway_v2 ./cmd/gateway
```

**2. Configure**

```
cp config.example.json config.json
```

Open `config.json` and change one thing: rename the key `sk-change-me` under `tenants` to a
random string of your own (for example `sk-` plus 32 hex characters). That string is the API
key your clients will send. Leave `dashboard_password` and `proxy_secret` empty — they are
generated and saved on first start.

**3. Run**

```
./gateway_v2 -config config.json -auths auths -ca-dir ca
```

**4. Add an account**

Open <http://localhost:8898/> (user `admin`, password = `dashboard_password` from
`config.json`) and sign in with Google from the dashboard. Credentials are stored in `auths/`.

> The Google sign-in redirects back to `http://localhost:8898/oauth/callback`, so
> keep `server.port` at `8898` and do the login on the machine where the gateway runs
> (or forward that port to it). The login also needs a Google OAuth client secret:
> set `server.google_oauth_client_secret` (and `google_oauth_client_id` if you use your
> own client) in `config.json`.

**5. Use it from Claude Code**

```
export ANTHROPIC_BASE_URL=http://localhost:8898
export ANTHROPIC_AUTH_TOKEN=sk-your-key-from-config
export CLAUDE_CODE_GATEWAY_HINT_HEADERS=1
claude
```

`CLAUDE_CODE_GATEWAY_HINT_HEADERS=1` makes Claude Code label its requests, so small helper
calls (titles, summaries) go to a fast tier instead of waiting behind heavy reasoning.

Quick check that it's alive:

```
curl -H "x-api-key: sk-your-key-from-config" http://localhost:8898/v1/models
```

## Configuration

Everything lives in `config.json` (see `config.example.json`).

| Setting (`server.`) | What it does |
|---|---|
| `port` | Plain HTTP API + dashboard. Keep `8898` for the Google login (see above). |
| `public_host` | Hostname/IP clients use to reach the gateway; added to the TLS certificate. |
| `model_overrides` | Opt-in: serve a Claude family (`sonnet`/`opus`/`haiku`) from a native model. |
| `tls_fingerprint` | `go` (default) or `chrome`. |
| `allow_history_truncation` | Drop the oldest turns instead of returning "prompt is too long". |
| `ttft_timeout_seconds`, `read_timeout_seconds`, `max_stream_duration_seconds` | Timeouts. |

Each entry under `tenants` is one API key: `default_model`, `strategy`
(`sticky`, `round_robin`, `highest_quota`), `thinking_budget`, and so on. You can also create
and delete keys from the dashboard.

## Running on a server

Set `server.public_host` to the server's address and put the gateway behind a firewall or a
reverse proxy with TLS — the dashboard is protected by a password, but it is not meant to be
exposed bare to the internet. The gateway also exposes an HTTPS listener (`port + 2`) signed
by its own CA in `ca/`, and a CONNECT proxy (`port + 1`).

## Keep your secrets out of git

`config.json`, `auths/`, `ca/` (holds the CA private key), logs, databases and request
captures are all git-ignored. Don't commit them, and don't paste your API keys into issues.

## Development

```
go build ./...
go vet ./...
go test ./...
```

Files stay under 400 lines, code is `gofmt`-clean, and every fix ships with a test. CI enforces
the format and a coverage floor.

## Status and disclaimer

This is an independent, unofficial project. It is not affiliated with or endorsed by
Anthropic or Google. You are responsible for following the terms of service of the accounts
and services you connect to it.
