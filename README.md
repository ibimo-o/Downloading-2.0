# Downloading 2.0 (dl2)

> An open-source download engine that splits files across multiple
> connections and mirrors, then lets downloaders of the same file share
> pieces with each other automatically — like BitTorrent, but with zero
> setup, starting from a plain HTTP link.

[![CI](https://github.com/ibimo-o/Downloading-2.0/actions/workflows/ci.yml/badge.svg)](https://github.com/ibimo-o/Downloading-2.0/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Reference](https://img.shields.io/badge/go.dev-reference-blue)](https://pkg.go.dev/github.com/ibimo-o/Downloading-2.0)

<!-- TODO before launch: replace this comment with a real terminal-capture
     GIF of dl2 running (e.g. via vhs https://github.com/charmbracelet/vhs
     or asciinema). A demo showing the live progress bar and the
     before/after speed difference is the single highest-leverage thing in
     this README for a first-time visitor -- people decide whether to keep
     reading within seconds. -->

A multi-connection, multi-source, peer-assisted download engine — built to
be measurably faster than traditional single-connection downloads.

**Measured results (see [Benchmarking](#benchmarking) below):** up to
**4.7x faster** than a standard single-connection download, with an
additional **~30-35% speedup** from peer-assisted swarm mode on top of
that.

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup,
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community guidelines, and
[SECURITY.md](SECURITY.md) for current known limitations — please read
the security notes before deploying the API server or tracker beyond
localhost/a trusted LAN.

## Why it's faster

- **Parallel byte-range requests** — one file gets pulled with many
  simultaneous connections instead of one, avoiding single-TCP-stream
  throughput caps and per-connection server throttling.
- **Dynamic work-queue scheduling (Phase 2)** — the file is split into many
  small pieces pulled from a shared queue by a worker pool, instead of
  fixed equal chunks. A slow piece only slows the worker handling it, not
  the whole job's tail end.
- **Adaptive multi-source weighting (Phase 2)** — when multiple mirror URLs
  are given, live throughput per source is tracked and new pieces are
  biased toward whichever source is currently fastest.
- **Peer-assisted swarm mode (Phase 4)** — downloaders of the same file can
  exchange already-downloaded pieces directly with each other, so the
  swarm gets faster as more people download the same file around the same
  time, with no torrent file or manual setup.
- **Integrity-first** — every piece (and the final file) can be SHA-256
  verified, so multi-source/multi-peer downloading doesn't risk silent
  corruption.

## Download

**Pre-built binaries** (no Go toolchain needed) are attached to each
[GitHub Release](https://github.com/ibimo-o/Downloading-2.0/releases) —
download the archive for your OS/architecture, extract it, and run `dl2`
(or `dl2.exe` on Windows) directly.

To build from source instead, see [Build](#build) below.

## Requirements

- Go 1.22+ (https://go.dev/dl/)
- Internet access

## Build

```bash
cd downloading2
go build -o dl2.exe ./cmd/dl2
go build -o benchmark.exe ./cmd/benchmark
go build -o tracker.exe ./cmd/tracker
```

## Basic usage (Phase 1-3: solo multi-connection download)

```bash
# Default: dynamic work-queue mode, 16 connections
.\dl2.exe -url https://example.com/bigfile.zip

# Custom output and connection count
.\dl2.exe -url https://example.com/bigfile.zip -out myfile.zip -connections 32

# Multiple mirrors of the same file
.\dl2.exe -url https://mirror1.com/file.zip,https://mirror2.com/file.zip

# With integrity verification
.\dl2.exe -url https://example.com/bigfile.zip -sha256 <expected_hash>

# Legacy Phase 1 fixed-chunk mode (for comparison)
.\dl2.exe -url https://example.com/bigfile.zip -queue=false -connections 16
```

## Benchmarking

```bash
.\benchmark.exe http://speedtest.tele2.net/100MB.zip
```

Runs a baseline single-connection download plus dl2 at 4/8/16/32
connections, 3 trials each, and prints an averaged comparison table with
speedup multipliers. Real measured results on a consumer Wi-Fi connection:

```
Method                           Avg(s)   Min(s)   Max(s)   Speedup  Fails
-----------------------------------------------------------------
Baseline (single connection)     267.70   261.51   277.62     1.00x      0
dl2 queue, 4 connections          83.80    78.46    87.69     3.19x      0
dl2 queue, 8 connections          56.78    54.14    61.68     4.71x      0
dl2 queue, 16 connections         57.77    55.07    60.42     4.63x      0
dl2 queue, 32 connections         57.13    52.13    60.80     4.69x      0
```

Speed gains scale up to ~8 connections then plateau, reflecting the actual
network ceiling rather than a flaw — a realistic, defensible finding for a
report or README, not a cherry-picked single run.

## Phase 4: P2P swarm mode

The headline feature: turn every download into an instant peer swarm, with
no torrent file and no manual setup. The first person to download a file
gets normal multi-source speed. Everyone after them gets faster, because
they can pull pieces from earlier downloaders instead of only the origin
server.

How it works:
- A small **tracker** server coordinates who has which pieces of which file
  (identified by a hash of the source URL). It never sees file content,
  only "peer X has pieces [3,7,12] of file Y".
- Each running `dl2` instance starts a tiny local **piece-server** and
  announces what it already has to the tracker every few seconds.
- Before pulling a piece from the origin URL, a worker asks the tracker
  whether any peer already has it. If yes, fetch from that peer (usually
  faster, and it offloads the origin server). If no peer has it, fall back
  to the origin as normal, then become a source for that piece.

### Running it

1. Start the tracker once, anywhere reachable by all peers (your own
   machine is fine for local testing):
```bash
.\tracker.exe -port 9090
```

2. Run two or more `dl2` instances pointed at the same file and tracker,
   each with a different `-listen` port:
```bash
.\dl2.exe -url <file-url> -tracker http://localhost:9090 -listen localhost:9091 -out copy1.zip
.\dl2.exe -url <file-url> -tracker http://localhost:9090 -listen localhost:9092 -out copy2.zip
```

Start the second one shortly after the first and it should finish faster,
since it can pull pieces the first instance already has.

Swarm mode is fully optional — omit `-tracker` and dl2 behaves exactly like
Phase 2/3 (origin-only, no peer involvement).

Every piece pulled from a peer is verified against a SHA-256 hash the peer
announced for it before being accepted — a corrupted or tampered peer
response fails verification and falls back to the origin automatically.

For shared/multi-user setups, add `-token` to require a shared secret on
the tracker and have `dl2` send it:
```bash
.\tracker.exe -port 9090 -token mysecret
.\dl2.exe -url <url> -tracker http://localhost:9090 -listen localhost:9091 -token mysecret
```

### Measuring the real swarm benefit (averaged, controlled)

Manually timing two terminal windows is noisy — Wi-Fi conditions can shift
between runs and swamp the actual swarm effect. `swarmbench` isolates it
properly: for several trials, it downloads the same file twice
**sequentially** in-process — copy1 with no peers available (true origin
baseline) immediately followed by copy2 (which can pull from copy1 via the
swarm) — using a cache-busting query string per trial so no trial's swarm
state leaks into the next one's "no peers" baseline.

```bash
go build -o swarmbench.exe ./cmd/swarmbench
.\swarmbench.exe http://speedtest.tele2.net/100MB.zip
```

Output looks like:

```
=========================================
 SWARM BENCHMARK SUMMARY
=========================================
Trials completed:        3/3
Avg copy1 (no peers):    78.40s
Avg copy2 (peer-assist): 51.20s
Avg speedup from swarm:  34.7%
Median speedup:          35.1%
=========================================
```

This is a self-contained binary — it runs its own in-process tracker, so
you don't need `tracker.exe` running separately for this one.

## How dl2 compares to existing tools

| Tool | Multi-connection | Multi-source | P2P/swarm | Auto-swarm from a plain URL |
|---|---|---|---|---|
| curl / wget / browser default | No | No | No | N/A |
| IDM | Yes (segments, one server) | No | No | No |
| **aria2** | Yes (up to 16+ connections) | Yes (manual mirror list) | Yes, but only for real `.torrent`/magnet files | **No** |
| BitTorrent / qBittorrent | Yes (per-peer) | N/A | Yes | No — requires a published `.torrent` and seeders |
| **dl2** | Yes | Yes (adaptive weighting) | Yes | **Yes** — no torrent file, no manual mirror list, swarm forms automatically around a plain HTTP/HTTPS URL |

Honest framing: dl2 does not claim to out-engineer aria2 on raw
multi-connection speed — aria2 is a mature, 15+ year old project and a
fair baseline to beat is "comparable, not necessarily faster." dl2's real
differentiator is that its swarm mode activates automatically the moment
two people download the same plain URL, with zero manual setup — aria2's
P2P capability only works if a `.torrent`/magnet already exists for that
file.

### Run the head-to-head yourself

```bash
go build -o compare.exe ./cmd/compare
.\compare.exe http://speedtest.tele2.net/100MB.zip
```

This runs baseline, dl2, and (if `aria2c` is installed and on PATH) aria2
with comparable settings, 3 trials each, and prints an averaged comparison
table. If aria2c isn't installed, it tells you how to install it
(`winget install aria2.aria2`) instead of silently skipping the comparison.

## Full test harness (every mode, every size)

`cmd/fulltest` is the one command to run before claiming "dl2 handles any
file size with any mode" — it exercises baseline, queue mode, and
adaptive mode against tiny/small/medium/large/huge files (up to 1GB),
plus a swarm copy1→copy2 pair on the 1GB file specifically (the size that
actually exercises the disk-backed piece-store fix), all in one run with
a consolidated report at the end.

```bash
go build -o fulltest.exe ./cmd/fulltest
.\fulltest.exe
```

Takes a while on a typical connection (1GB downloaded 5 times total across
modes, plus the 1GB swarm pair = ~7GB transferred). Useful as a smoke test
after any engine change, and as the source of real size-range data for a
README/report table.

## Project layout

```
downloading2/
  cmd/dl2/main.go              CLI entry point
  cmd/benchmark/main.go        Automated benchmark suite
  cmd/tracker/main.go          Phase 4 swarm coordinator server
  cmd/swarmbench/main.go       Averaged, controlled swarm-speedup benchmark
  cmd/server/main.go           Phase 5 HTTP API server
  cmd/compare/main.go          Head-to-head benchmark vs aria2 and baseline
  pkg/dl2/dl2.go               Phase 5 public Go SDK
  browser-extension/           Phase 5 Chrome/Edge MV3 extension
  internal/chunk/chunk.go      Chunk/piece planning + SHA-256 verification
  internal/engine/engine.go    Phase 1 fixed-chunk engine (legacy mode)
  internal/engine/queue.go     Phase 2/4 dynamic work-queue engine + swarm hooks
  internal/engine/adaptive.go  Phase 6 adaptive engine (scaling, sizing, stragglers, latency)
  internal/swarm/swarm.go      Phase 4 peer piece-store, piece-server, tracker client
```

## Phase 5: SDK, API server, and browser extension

Phase 5 wraps the same core engine three more ways so it's usable beyond
the CLI.

### 5a. Public Go SDK

`pkg/dl2` is the stable, importable package for other Go projects:

```go
import "github.com/ibimo-o/Downloading-2.0/pkg/dl2"

err := dl2.Download(ctx, dl2.Options{
    URL:    "https://example.com/file.zip",
    Output: "file.zip",
})

// or with live progress:
progressCh := make(chan dl2.Progress)
go dl2.DownloadWithProgress(ctx, opts, progressCh)
for p := range progressCh {
    fmt.Println(p.DownloadedBytes, "/", p.TotalBytes)
}
```

### 5b. HTTP API server

Exposes the engine over REST so anything — a browser extension, a web app,
a curl script, your future Revenue Pulse / DomainSearch.ai stack — can
trigger and monitor downloads without touching Go.

```bash
go build -o server.exe ./cmd/server
.\server.exe -port 8787
```

```bash
# start a download
curl -X POST http://localhost:8787/download \
  -H "Content-Type: application/json" \
  -d '{"url":"http://speedtest.tele2.net/100MB.zip","connections":16}'
# -> {"job_id":"job-1"}

# poll progress
curl http://localhost:8787/status/job-1

# list all jobs
curl http://localhost:8787/jobs
```

### 5c. Browser extension

`browser-extension/` is a Chrome/Edge MV3 extension that intercepts
downloads above 5MB and routes them through the local API server instead of
the browser's default single-connection downloader, with a popup showing
live progress for active jobs.

To load it:
1. Make sure `server.exe` is running (`.\server.exe -port 8787`)
2. Go to `chrome://extensions` (or `edge://extensions`)
3. Enable "Developer mode"
4. Click "Load unpacked", select the `browser-extension/` folder
5. Trigger a download of a file >5MB on any site — it should be intercepted
   and routed through dl2 automatically

Note: the extension folder doesn't include an icon file yet
(`icon48.png`) — add any 48x48 PNG there before loading, or remove the
`icons` block from `manifest.json` if you'd rather skip it for now.

If the API server isn't running, the extension fails silently and the
browser's normal download proceeds untouched — it never blocks a download
the user is trying to make.

## Phase 7: unified adaptive + swarm engine

`-adaptive` mode was originally built separately from swarm mode (Phase 6
standalone), specifically to avoid risking the working swarm code while
testing new scheduling logic. As of Phase 7, they're merged into one
engine — `-adaptive` now supports `-tracker`/`-listen`/`-token` directly,
so you get dynamic scaling, adaptive sizing, straggler handling, and
latency-aware sourcing *together with* peer-assisted swarm downloading,
instead of having to choose one or the other.

**The technical problem that caused the split, and how it's resolved:**
swarm mode requires every peer to agree on identical piece boundaries —
"piece #7" has to mean the same byte range for every downloader, or peers
can't match and verify each other's pieces. The original adaptive engine
generated pieces lazily, sized per-downloader based on their own measured
speed — two adaptive downloaders would split the same file completely
differently, making peer matching meaningless. Phase 7 fixes this with a
fixed, shared "grid" of small pieces (`-grid-piece-kb`, default 1MB) that
every downloader and peer agrees on. Workers still claim multiple grid
pieces at once in a single batched HTTP request, sized adaptively by
measured throughput — fast sources grab big batches, slow ones grab one
grid piece at a time — but the batch is split back into individual grid
pieces afterward, each hashed and stored separately, so it stays fully
swarm-compatible.

Four techniques, now all available together with swarm:

1. **Dynamic connection scaling** — starts with a small worker pool
   (`-min-workers`, default 4) and grows it while aggregate throughput
   keeps meaningfully improving, stopping once gains flatten. Shrinks back
   down if throughput regresses.
2. **Adaptive batch sizing on a fixed grid** — batches are sized per
   request based on that source's recent measured throughput, but always
   in units of the shared grid, keeping swarm compatibility.
3. **Straggler redundant-fetch** — if a batch is taking far longer than
   expected given current throughput, an extra worker is spawned to
   relieve pressure rather than literally splitting the stuck range.
4. **Latency-aware source scoring** — each source's time-to-first-byte is
   tracked and factored into source selection alongside throughput, load,
   and error rate.
5. **Peer-assisted swarm, merged in** — before claiming a batch from the
   origin, a worker checks whether any peer already has the next grid
   piece; if so, it's fetched directly from that peer (hash-verified
   against the announced SHA-256, exactly like Phase 4) instead of going
   to the origin at all.

```bash
.\dl2.exe -url <url> -adaptive
.\dl2.exe -url <url> -adaptive -min-workers 4 -max-workers 32
.\dl2.exe -url <url> -adaptive -tracker http://localhost:9090 -listen localhost:9091
```

**Honest, measured tradeoff** (see `cmd/fulltest` results): adaptive mode
won clearly at 100MB scale (~15% faster than fixed queue mode) but lost to
fixed queue mode at 1GB scale (queue: 478.9s vs adaptive: 538.2s on one
real test run) — the constant re-evaluation/rescaling that helps on
medium files adds cumulative overhead on very long-running transfers.
Treat "adaptive is always better" as unproven; it depends on file size and
network stability. Run `cmd/fulltest` or `cmd/compare` on your own network
to see which mode wins for your actual conditions.

## File size range: tiny to huge

Two things matter if dl2 is going to handle "any file, any size" rather
than just large test files:

- **Tiny files (<4MB)** automatically bypass chunking entirely and use a
  plain single GET — multi-connection overhead isn't worth it below a
  certain size, and forcing it would make dl2 *slower* than a normal
  download for small files. This fast path applies in both `-queue` and
  `-adaptive` modes.
- **Huge files (GB-TB scale)**: the swarm piece cache (`internal/swarm`)
  is disk-backed, not in-memory — only piece hashes (a few dozen bytes
  each) are kept in memory regardless of file size, so swarm mode doesn't
  risk OOM-ing on very large files. (Note: full in-memory job state on the
  API server and lack of resume-after-crash are still open gaps — see
  "What's next" below.)

## What's next

Toward the longer-term vision (infrastructure other apps depend on, not
just a download manager):

- **Resume-after-crash** — persist piece progress to disk so a killed
  process can pick up where it left off instead of restarting, critical
  for very large files.
- **Job persistence in the API server** — currently in-memory, lost on
  restart; infrastructure other apps depend on needs durable job state.
- **Content-addressable swarm matching** — currently peers are matched by
  hash of the *URL*; matching by hash of file *content* would let the
  swarm share pieces across different URLs/mirrors pointing at the same
  file, which is closer to how a real CDN cache works.
- NAT traversal for swarm peers across different networks (current
  version assumes peers can reach each other directly — fine for
  LAN/same-network testing; real deployment would need STUN/TURN or
  relay support).
- Smarter swarm trust: weight peer vs. origin choice by measured peer
  speed, not just availability.
- Package the extension for the Chrome Web Store (requires a real icon,
  privacy policy, and review).

## Notes on this build

This code was written and reviewed in a sandbox without a Go toolchain or
internet access, so it could not be compiled there. Build and test it on
your own machine — confirm with `go build` and a real download before
relying on it.
