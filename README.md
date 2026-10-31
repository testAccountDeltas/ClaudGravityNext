# ClaudGravityNext

A gateway that lets Claude Code and Anthropic-API clients talk to Gemini-family models
behind a pool of accounts. It accepts the Anthropic Messages API (`/v1/messages`,
`count_tokens`, `models`, streaming SSE), translates requests to the upstream Gemini
format and translates the answers back, including tools, thinking, images and PDFs.

## Highlights

- Messages API wire compatibility: SSE grammar, usage, stop reasons, Anthropic error types.
- Tools: schema translation, argument repair, history re-declaration, long MCP names.
- Prompt-cache friendliness: deterministic request prefix, `max_tokens: 0` pre-warm,
  prefix-change log.
- Effort levels mapped to thinking budgets (low / medium / high / xhigh / max).
- Account pool: affinity persisted across restarts, burst spreading, quota-aware
  cooldowns, automatic failover, disabled accounts are never queried.
- Opt-in `server.model_overrides` to serve a Claude family from a native third-party model.

## Build and test

```
go build ./...
go vet ./...
go test ./...
```

Conventions: Go files stay under 400 lines, `gofmt` clean, every fix ships with a test.

## Quick start

1. Build: `go build -o gateway_v2 ./cmd/gateway` (add `.exe` on Windows).
2. Create your config from the template and edit it:
   ```
   cp config.example.json config.json
   ```
   - Replace the `sk-change-me` key (the object key under `tenants`) with your own random
     string, e.g. `sk-` followed by 32 hex characters. This is the API key your clients send.
   - Leave `dashboard_password` and `proxy_secret` empty: they are generated and written back
     to `config.json` on first start.
   - `google_oauth_client_id` / `google_oauth_client_secret` are optional overrides for the
     OAuth client used to log accounts in; leave empty to use the built-in defaults.
3. Start it:
   ```
   ./gateway_v2 -config config.json -auths auths -ca-dir ca
   ```
   `-connect-port` sets the CONNECT-proxy port (default `server.port + 1`).
4. Open `http://localhost:8901/` (user `admin`, password = `dashboard_password` from
   `config.json`) and add your Google account(s) through the OAuth login. Credentials are
   stored under `auths/`.
5. Point Claude Code at the gateway:
   ```
   ANTHROPIC_BASE_URL=http://localhost:8901
   ANTHROPIC_AUTH_TOKEN=<the tenant key from config.json>
   CLAUDE_CODE_GATEWAY_HINT_HEADERS=1
   ```
   `CLAUDE_CODE_GATEWAY_HINT_HEADERS=1` makes Claude Code send the request-class headers the
   gateway uses to route small helper requests (titles, summaries) to a cheap tier.

`config.json`, `auths/`, `ca/` (the gateway's own MITM CA key), logs and request captures are
git-ignored; never commit them. Running on a remote host: set `server.public_host` to its
address so the generated TLS certificate matches, and put the gateway behind your own firewall
or reverse proxy.

Other `server` options: ports and timeouts, `tls_fingerprint` (`go` or `chrome`),
`model_overrides`, `allow_history_truncation`, `max_stream_duration_seconds`.

## Documentation

Detailed design notes, audit resolutions and deployment procedures are kept outside git by
repository policy (only this file is tracked).
