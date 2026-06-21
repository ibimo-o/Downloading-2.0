# Security Policy

## Reporting a vulnerability

If you find a security issue, please open a GitHub issue tagged
`security` (this project doesn't yet have a dedicated security contact —
for a low-traffic early-stage project, a public issue is fine unless the
vulnerability is actively exploitable against real deployments, in which
case please say so in the issue and avoid public exploit details until
it's addressed).

## Resolved since initial release

### Per-piece integrity verification in swarm mode — FIXED
Every piece fetched from a peer is now verified against a SHA-256 hash
the peer announced for it via the tracker, before being accepted. If a
peer serves corrupted or tampered data, the hash check fails and the
piece is fetched from the origin server instead. Whole-file `-sha256`
verification is still recommended as a final check, but per-piece
verification means a single bad peer can no longer silently corrupt your
download.

### No authentication on the tracker/API server — PARTIALLY FIXED
Both `cmd/tracker` and `cmd/server` now support an optional `-token` flag.
When set, all requests must include header `X-DL2-Token` matching it;
unauthenticated requests get `401 Unauthorized`. This is shared-secret
auth, not full identity/authorization — adequate for a small trusted group
sharing one token, not for a public multi-tenant deployment. Auth is
**off by default** (empty token), so existing localhost/LAN workflows are
unaffected unless you opt in.

```bash
.\tracker.exe -port 9090 -token mysecret
.\dl2.exe -url <url> -tracker http://localhost:9090 -listen localhost:9091 -token mysecret
.\server.exe -port 8787 -token mysecret
```

### SSRF protection on the API server — FIXED
`POST /download` now resolves and validates the target URL (and any
mirrors) before fetching, rejecting addresses that resolve to loopback,
private, link-local, or cloud-metadata IP ranges (including the
well-known `169.254.169.254` metadata endpoint). Returns `403 Forbidden`
for rejected URLs. Can be bypassed with `-allow-private` for local
dev/testing only — **never enable this on a publicly reachable
deployment**, it disables the protection entirely.

### Rate limiting on the API server — PARTIALLY FIXED
`/download` is now rate-limited per caller IP (token bucket, default 5
req/s with a burst of 10, configurable via `-rate-limit`/`-rate-burst`).
This is a basic safeguard against accidental hammering or naive abuse on
a small deployment, not a substitute for a real API gateway/WAF under
serious public load. The tracker (`cmd/tracker`) is not yet rate-limited.

## Current known limitations

Being upfront about these matters more than pretending they don't exist.
This is an early-stage project — treat it accordingly.

### No authentication on the local API server or tracker (`cmd/server`, `cmd/tracker`)
Both bind to `localhost` by default. Shared-secret `-token` auth is
available (see "Resolved" above) but is opt-in, not enforced — **you must
explicitly set `-token` for it to apply.** Without it, anyone who can
reach the API server can instruct it to download (now SSRF-validated)
URLs to your disk, and anyone who can reach the tracker can pollute the
swarm with bogus peer addresses. Still do not expose either publicly
without setting a token at minimum, and ideally a real reverse-proxy auth
layer too.

### Tracker has no rate limiting yet
Unlike the API server's `/download` endpoint, `cmd/tracker`'s
`/announce` and `/peers` endpoints are not yet rate-limited.

### NAT traversal is not implemented
Swarm peers must be directly reachable at the address they announce.
Works on a LAN or localhost; does not work across most home/consumer NATs
without manual port forwarding. No STUN/TURN/relay support yet.

## Recommended safe usage today

- Run `server.exe` / `tracker.exe` only on `localhost` or a trusted LAN.
- Always pass `-sha256` (whole-file hash) when using swarm mode with
  peers you don't control.
- Don't put this behind a public-facing reverse proxy without adding auth,
  input validation on URLs (e.g. blocking internal/private IP ranges), and
  rate limiting first.
