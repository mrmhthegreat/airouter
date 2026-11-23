# Security Policy

## Reporting a vulnerability

If you discover a security issue in airouter, please report it privately
instead of opening a public issue.

**Email:** security@<your-domain>.<tld>   ← replace this with a real address
**PGP:**   <optional fingerprint>         ← delete this line if you don't use PGP

Please include:

1. A description of the issue and its impact
2. The exact version / commit you tested (run `airouter -v`)
3. A minimal reproduction (a script, a curl command, an HTTP request — anything
   we can run to see the issue)
4. Any suggested fix

We'll acknowledge within 48 hours and aim to ship a fix within 30 days. Once
the fix is released we'll publish a GitHub Security Advisory crediting you
(unless you'd prefer to remain anonymous).

## What counts as a security issue

- The encrypted key store can be decrypted without `$AIROUTER_SECRET`
- A tenant can read or modify another tenant's providers, keys, or usage
- An unauthenticated caller can hit an admin endpoint (`/v1/providers*`,
  `/v1/bootstrap`, `/v1/tenants*`, etc.) by sending something other than the
  admin token
- A tenant can escape the `TenantPolicy` limits (max providers, custom
  base URLs, Ollama access)
- HTTP request smuggling or SSRF via a tenant's `base_url`
- Path traversal via the transcribe / speak / sample endpoints
- Any RCE in the request path

## What doesn't count

- Issues that require an attacker to already have the admin token
- Issues that require an attacker to already have the encryption secret
- Bugs in upstream Go dependencies — report those to the upstream project
  (but DO tell us so we can bump the version in `go.mod` and release a fix)
- Performance issues, crashes on malformed input that don't leak data,
  feature requests

## Continuous scanning

Every push and every PR runs three security scanners in CI (see
`.github/workflows/security.yml`):

- **govulncheck** — official Go vulnerability database, scans the module graph
  and the actually-imported packages
- **gosec** — static security analyzer for Go code
- **CodeQL** — GitHub's semantic code analysis, results in the Security tab

Dependabot (`.github/dependabot.yml`) opens PRs automatically when a
dependency or GitHub Action has a security advisory or a new release.
