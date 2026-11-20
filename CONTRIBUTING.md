# Contributing to airouter

Thanks for considering a contribution! airouter is a small project and the bar
for changes is correspondingly small, but please follow the rules below so the
project stays consistent and reliable.

## Quick ground rules

- Be kind. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
- Don't share real API keys in issues, PRs, or screenshots.
- Don't suggest creating multiple accounts to multiply a free quota — that
  breaks every provider's terms of service and we will not accept changes that
  encourage it.
- Tests must pass (`make test`) before a PR is reviewed.
- Keep dependencies minimal. Anything new in `go.mod` needs a comment in the
  PR description explaining why stdlib is not enough.

## Project layout

```
cmd/airouter/         CLI entry point and per-command files (ask.go, serve.go, …)
pkg/types/            public data shapes (ProviderConfig, Tenant, Request, …)
pkg/store/            Store interface, MemoryStore, SQLStore
pkg/preset/           built-in provider catalog + curated free-pool model list
pkg/router/           core router: provider selection, failover, classification
pkg/server/           HTTP handler
examples/             runnable usage examples (shell + Go)
```

The split is intentional: `pkg/types` has no dependencies; `pkg/store`,
`pkg/preset` depend on `types`; `pkg/router` depends on all three; `pkg/server`
depends on `router`. Don't add cycles.

## Adding a new provider preset

1. Add an entry to `Definitions` in `pkg/preset/preset.go`.
2. If the provider is in the curated free pool, also add one or more
   `FreeModel` entries to `FreePool` with current RPM/RPD/TPM and a
   `VerifiedOn` date.
3. Run `go test ./pkg/preset/...` —
   `TestDefinitionsContainsAllPresetsReferencedByFreePool` will catch a missing
   definition.
4. Add the env var name to `.env.example` if you add one.

## Updating free-tier limits

Free-tier numbers move. The catalog has a `VerifiedOn` field on every entry
and `CatalogVerifiedOn()` returns the newest one. If you are checking limits:

1. Open the provider's own rate-limit docs page (linked in
   `pkg/preset/preset.go` next to each entry).
2. Update RPM/RPD/TPM and the `VerifiedOn` date.
3. Note the source URL in your PR description.
4. Run `make test`.

## Tests

- `pkg/router/router_test.go` covers failover, daily-limit pause with timezone
  reset, JSON validation + failover, Gemma quirks, tenant limits, spread
  strategy, persistence across "restarts", and the new bootstrap command.
- `pkg/server/server_test.go` covers the HTTP API end-to-end.
- `pkg/store/store_test.go` and `sqlstore_test.go` cover both store
  implementations.
- `pkg/preset/preset_test.go` covers Apply, the catalog, and FreePool.

Add a test for any new behaviour — the existing tests are intentionally
parallel to the code so reading them is the fastest way to understand what a
function does.

## Commit messages

Use the [Conventional Commits](https://www.conventionalcommits.org/) style:

```
feat(router): add a "round-robin" strategy
fix(server): preserve Retry-After header on 503
docs(preset): refresh Groq free-tier limits, verified 2026-11-15
chore(deps): bump modernc.org/sqlite to v1.34.5
```

## Pull request flow

1. Fork → branch from `main`.
2. Make your change. Run `make test` (or `go test ./...`).
3. If you changed the CLI or HTTP API, update `README.md` and the relevant
   example in `examples/`.
4. Open the PR with a description that answers: what does this change, why,
   and how was it tested?

## Reporting bugs

Open an issue with:

- `airouter` version (or git commit)
- OS and architecture
- The exact command or HTTP request you ran
- The full stderr output (it has the `[airouter]` log prefix)
- The relevant rows from `airouter provider list` (redact nothing — provider
  configs never contain API keys, only env var names)
