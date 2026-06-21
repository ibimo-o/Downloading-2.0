# Contributing to Downloading 2.0

Thanks for considering a contribution. This project is young and the
architecture is still settling, so please open an issue to discuss
nontrivial changes before sending a large PR — saves everyone time.

## Development setup

```bash
git clone https://github.com/ibimo-o/Downloading-2.0.git
cd Downloading-2.0
go build -o dl2 ./cmd/dl2
go test ./...
```

Requires Go 1.22+.

## Project structure

- `internal/chunk` — piece/chunk planning and SHA-256 verification
- `internal/engine` — the download engines (Phase 1 fixed-chunk, Phase 2+
  dynamic work-queue with swarm hooks)
- `internal/swarm` — Phase 4 peer-to-peer piece sharing
- `pkg/dl2` — the public, stable SDK surface — please keep this package's
  exported API backward-compatible where possible
- `cmd/*` — CLI entry points (downloader, tracker, benchmarks, API server)
- `browser-extension/` — Chrome/Edge MV3 extension

## Before submitting a PR

1. `go vet ./...` and `go test ./...` should both pass cleanly.
2. Add tests for new logic in `internal/chunk` or `internal/engine` where
   feasible — these packages have the most reusable, testable logic.
3. Keep `pkg/dl2`'s public API additive when possible; breaking changes
   there affect anyone depending on the SDK.
4. Update `README.md` if you add a flag, endpoint, or new phase/feature.

## Reporting bugs

Open an issue with: your OS, Go version, the exact command you ran, and
the full output/error. For download failures, including the target URL
(if it's not sensitive) helps a lot.

## Known rough edges (good first contributions)

- No persistence for API server job history (in-memory, lost on restart)
- No authentication on the local API server or tracker (fine for
  localhost-only use today, a real concern if ever exposed beyond that)
- Swarm peer selection is "first peer with the piece," not "fastest peer"
- No NAT traversal for swarm peers across different networks
- Browser extension has no real icon yet

See `SECURITY.md` for the security-specific limitations before relying on
this in any non-local/non-trusted-network setup.
