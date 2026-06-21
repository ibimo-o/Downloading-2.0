# Changelog

All notable changes to this project are documented here.

## [1.0.0] - 2026-06-21

First public release.

### Core engine
- **Phase 1**: Multi-connection chunked download engine with per-chunk
  retry/backoff and SHA-256 integrity verification.
- **Phase 2**: Dynamic work-queue scheduler replacing fixed chunk
  assignment, plus adaptive multi-source weighting based on live
  per-mirror throughput.
- **Phase 3**: Automated, multi-trial benchmark suite (`cmd/benchmark`)
  comparing dl2 against a standard single-connection baseline across
  connection counts.
- **File-size range handling**: files under 4MB bypass chunking entirely
  (plain single GET) — multi-connection overhead was making tiny files
  slower than a normal download. Verified working from ~15KB up to 1GB
  files with no crashes or OOM.

### Swarm mode (peer-to-peer)
- **Phase 4**: Peer-assisted swarm — a lightweight tracker (`cmd/tracker`),
  per-instance piece-server, and peer-vs-origin race logic so later
  downloaders of the same file benefit from earlier downloaders' progress.
  No `.torrent` file or manual mirror list needed — the swarm forms
  automatically around a plain HTTP/HTTPS URL.
- Per-piece SHA-256 hash verification: peers announce a hash alongside
  each piece; the receiving side verifies fetched bytes before accepting,
  falling back to the origin on mismatch.
- Disk-backed piece cache (`internal/swarm.Store`) — only piece hashes
  stay in memory regardless of file size, so swarm mode doesn't risk
  OOM-ing on very large files.
- `cmd/swarmbench`: a dedicated, controlled benchmark isolating the swarm
  speedup from network noise (sequential copy1→copy2 trials, averaged).

### Adaptive scheduling, merged with swarm
- **Phase 6**: dynamic connection scaling, adaptive piece sizing,
  straggler redundant-fetch, and latency-aware source scoring
  (`-adaptive` mode).
- **Phase 7**: merged the adaptive engine with swarm mode into one unified
  engine — `-adaptive` now supports `-tracker`/`-listen`/`-token`
  directly. Resolved by introducing a fixed, shared piece "grid" that all
  downloaders/peers agree on, while still batching adaptively sized
  groups of grid pieces per HTTP request.

### Distribution surfaces
- **Phase 5**: public Go SDK (`pkg/dl2`), HTTP API server (`cmd/server`)
  exposing the engine over REST, and a Chrome/Edge MV3 browser extension
  that intercepts large downloads and routes them through dl2.
- `cmd/fulltest`: one command exercising every mode (baseline, queue,
  adaptive, swarm) across the full size range (tiny → 1GB) with a
  consolidated report.
- `cmd/compare`: head-to-head benchmark against aria2 (the closest mature
  competitor) and baseline.

### Security
- Optional shared-secret auth (`-token`) on the tracker and API server.
- SSRF protection on the API server: `POST /download` resolves and
  validates target URLs (and mirrors), rejecting loopback, private,
  link-local, and cloud-metadata addresses before fetching. Bypassable
  with `-allow-private` for local dev/testing only.
- Per-IP rate limiting on the API server's `/download` endpoint.

### Project infrastructure
- Apache 2.0 license, CI workflow (build/vet/test/cross-platform compile
  checks), automated release workflow (cross-platform binaries attached
  to GitHub Releases on version tags), unit tests for the chunk-planning
  package, CONTRIBUTING.md, CODE_OF_CONDUCT.md, issue/PR templates,
  SECURITY.md with honest known-limitations disclosure.

### Measured benchmarks (consumer Wi-Fi, real test sessions — see
`cmd/benchmark`/`cmd/swarmbench`/`cmd/fulltest` to reproduce on your own
network)
- Up to **4.7x** faster than a standard single-connection download
  (queue mode, 8-16 connections, 100MB file).
- **3.4-3.8x** faster at 1GB scale (queue: 478.9s vs. baseline: 1826.1s).
- **30-45%** additional speedup from swarm mode for a "second downloader"
  at 100MB; **4.6%** at 1GB (smaller gain at scale — an open question,
  not yet root-caused).
- **15%** speedup from the Phase 7 unified adaptive+swarm engine over
  origin-only, first real test.

### Known limitations (see SECURITY.md for full detail)
- No resume-after-crash for interrupted downloads.
- API server job state is in-memory, lost on restart.
- Swarm peers matched by hash of the *URL*, not file *content* — two
  different URLs serving the same file won't share a swarm yet.
- No authentication enforced by default on the API server or tracker —
  `-token` is opt-in.
- No NAT traversal for swarm peers across different networks.
- Browser extension ships without a real icon.
- Tracker's `/announce` and `/peers` endpoints are not yet rate-limited
  (API server's `/download` is).
