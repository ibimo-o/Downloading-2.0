# Changelog

All notable changes to this project are documented here.

## [Unreleased]

### Changed
- **Phase 7: merged adaptive and swarm engines.** `-adaptive` mode now
  supports `-tracker`/`-listen`/`-token` directly -- dynamic scaling,
  adaptive sizing, straggler handling, and latency-aware sourcing now work
  *together with* peer-assisted swarm downloading, instead of being two
  separate engines. Resolved by introducing a fixed, shared piece "grid"
  that all downloaders/peers agree on, while still batching adaptively
  sized groups of grid pieces per HTTP request. `AdaptiveOptions` field
  `MinPieceBytes`/`MaxPieceBytes` replaced with `GridPieceSize`/
  `MaxBatchSize` -- this is a breaking change to the SDK's adaptive
  options if you were setting those fields directly.

### Added
- **File-size range handling.** Files under 4MB now bypass chunking
  entirely (plain single GET) in both `-queue` and `-adaptive` modes --
  multi-connection overhead was making tiny files slower than a normal
  download. Swarm piece cache (`internal/swarm.Store`) is now disk-backed
  instead of in-memory, so it no longer risks OOM-ing on very large
  (GB-TB scale) files -- only piece hashes stay in memory regardless of
  file size.
- **Phase 6 (community-suggested): adaptive engine.** New `-adaptive` mode
  alongside the existing Phase 2 work-queue engine, adding dynamic
  connection scaling, adaptive piece sizing, straggler redundant-fetch,
  and latency-aware source scoring. Not yet benchmarked against Phase
  2/3 -- treat as experimental until measured.

### Fixed
- **SSRF protection on the API server.** `POST /download` now resolves
  and validates target URLs (and mirrors), rejecting loopback,
  private, link-local, and cloud-metadata addresses before fetching.
  Bypassable with `-allow-private` for local dev/testing only.
- **Per-IP rate limiting on the API server's `/download` endpoint**
  (token bucket, configurable via `-rate-limit`/`-rate-burst`).
- **Per-piece hash verification in swarm mode.** Peers now announce a
  SHA-256 hash alongside each piece index; the receiving side verifies
  fetched bytes against it before accepting, falling back to the origin
  server on mismatch instead of silently accepting bad data.
- **Optional shared-secret auth on tracker and API server.** New `-token`
  flag on `cmd/tracker` and `cmd/server`; when set, requests must include
  a matching `X-DL2-Token` header. Off by default — opt-in, doesn't break
  existing localhost workflows.

## [0.1.0] - Initial release

### Added
- **Phase 1**: Core multi-connection chunked download engine with
  per-chunk retry/backoff and SHA-256 integrity verification.
- **Phase 2**: Dynamic work-queue scheduler replacing fixed chunk
  assignment, plus adaptive multi-source weighting based on live
  per-mirror throughput.
- **Phase 3**: Automated, multi-trial benchmark suite (`cmd/benchmark`)
  comparing dl2 against a standard single-connection baseline across
  connection counts.
- **Phase 4**: Peer-assisted swarm mode — a lightweight tracker
  (`cmd/tracker`), per-instance piece-server, and peer-vs-origin race
  logic so later downloaders of the same file benefit from earlier
  downloaders' progress. Includes a dedicated controlled benchmark
  (`cmd/swarmbench`) isolating the swarm speedup from network noise.
- **Phase 5**: Public Go SDK (`pkg/dl2`), HTTP API server (`cmd/server`)
  exposing the engine over REST, and a Chrome/Edge MV3 browser extension
  that intercepts large downloads and routes them through dl2.
- Apache 2.0 license, CI workflow, unit tests for the chunk-planning
  package, CONTRIBUTING.md, SECURITY.md with honest known-limitations
  disclosure.

### Known limitations (see SECURITY.md for full detail)
- No authentication on the local API server or tracker — localhost/trusted
  LAN use only.
- No per-piece integrity verification in swarm mode yet (whole-file
  `-sha256` verification is available and recommended).
- No NAT traversal for swarm peers across different networks.
- Browser extension ships without a real icon.

### Measured benchmarks (consumer Wi-Fi, single test session)
- Up to **4.7x** faster than a standard single-connection download
  (Phase 2/3 work-queue mode, 8-32 connections).
- An additional **~30-35%** speedup from swarm mode for a "second
  downloader" pulling pieces from an earlier downloader (Phase 4).

These numbers are from one real but limited test environment — see
`cmd/benchmark` and `cmd/swarmbench` to reproduce on your own network and
hardware.
