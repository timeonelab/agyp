# Security Policy & Privacy Design

## Threat Model & Local Guarantees

- **Loopback-Only Binding**: The gateway listens strictly on `127.0.0.1` (`localhost`) and never binds `0.0.0.0`.
- **CSRF Protection on Control Endpoints**: State-mutating routes under `/_agyp/*` validate `Origin` / `Referer` against loopback origins and return `403 Forbidden` to cross-site browser requests. Set `AGYP_ADMIN_TOKEN` to also require an `X-Agyp-Token` header for programmatic local callers.
- **Credential Hygiene**: `~/.agyp/oauth.json` and `~/.agyp/profiles.json` are written atomically with `0600` (`umask 077`) permissions. No OAuth client secrets are bundled in source code or binaries.
- **Metadata-Only Telemetry**: `latency.jsonl` and `metrics.json` record timestamps, HTTP status codes, latencies, model IDs, and token counters only—never prompts, file paths, or completion text.

## Reporting a Vulnerability

If you discover a security issue (such as credential leakage, loopback bypass, or CSRF bypass), please **do not** open a public issue with exploit details. Report it privately through GitHub Security Advisories ("Report a vulnerability" in the repository's **Security** tab).
