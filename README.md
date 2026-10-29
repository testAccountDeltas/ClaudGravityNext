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

## Run

```
gateway_v2 -config config.json -auths auths -ca-dir ca -connect-port <port>
```

Configuration lives in `config.json` (`server` section: ports, timeouts,
`tls_fingerprint`, `model_overrides`, ...). Secrets are generated per instance on first run
and are never committed.

## Documentation

Detailed design notes, audit resolutions and deployment procedures are kept outside git by
repository policy (only this file is tracked).
