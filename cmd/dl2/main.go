package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ibimo-o/Downloading-2.0/internal/engine"
)

func main() {
	url := flag.String("url", "", "URL to download (required). Comma-separate multiple mirrors for the same file.")
	out := flag.String("out", "", "Output file path (defaults to filename from URL)")
	conns := flag.Int("connections", 16, "Number of parallel connections/chunks (Phase 1 fixed mode)")
	sha := flag.String("sha256", "", "Optional expected SHA-256 of the final file, for integrity verification")
	queueMode := flag.Bool("queue", true, "Use Phase 2 dynamic work-queue scheduler (default true). Set -queue=false for legacy Phase 1 fixed chunking.")
	adaptive := flag.Bool("adaptive", false, "Phase 6: use the adaptive engine (dynamic worker scaling, adaptive piece sizing, straggler redundant-fetch, latency-aware sourcing) instead of the fixed Phase 2 work-queue. Overrides -queue and -connections.")
	minWorkers := flag.Int("min-workers", 4, "Adaptive mode: starting/minimum worker count")
	maxWorkers := flag.Int("max-workers", 64, "Adaptive mode: maximum worker count")
	pieceSizeMB := flag.Int("piece-mb", 2, "Piece size in MB for queue mode (smaller = more adaptive, slightly more overhead)")
	tracker := flag.String("tracker", "", "Phase 4: swarm tracker URL (e.g. http://localhost:9090) to enable peer-assisted downloading. Empty disables swarm mode.")
	listen := flag.String("listen", "localhost:9091", "Phase 4: address this instance's piece-server listens on, so peers can pull pieces from you")
	token := flag.String("token", "", "optional shared-secret token, required if the tracker enforces auth (see tracker -token)")
	flag.Parse()

	if *url == "" {
		fmt.Println("usage: dl2 -url <url> [-out path] [-connections 16] [-sha256 <hash>]")
		os.Exit(1)
	}

	urls := strings.Split(*url, ",")
	for i := range urls {
		urls[i] = strings.TrimSpace(urls[i])
	}

	outPath := *out
	if outPath == "" {
		outPath = filepath.Base(urls[0])
	}

	e := engine.New(engine.Options{
		URLs:        urls,
		OutputPath:  outPath,
		Connections: *conns,
		ExpectedSHA: *sha,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Print live progress on a single updating line.
	go func() {
		for p := range e.Progress() {
			pct := 0.0
			if p.TotalBytes > 0 {
				pct = float64(p.DownloadedBytes) / float64(p.TotalBytes) * 100
			}
			chunksStr := fmt.Sprintf("%d/%d", p.ChunksDone, p.ChunksTotal)
			if p.ChunksTotal == 0 {
				// Adaptive mode generates pieces lazily, so total count
				// isn't known ahead of time -- show pieces completed only.
				chunksStr = fmt.Sprintf("%d pieces", p.ChunksDone)
			}
			fmt.Printf("\r[%5.1f%%]  %8.2f MB / %8.2f MB   %7.2f MB/s   %s   ",
				pct,
				float64(p.DownloadedBytes)/1e6,
				float64(p.TotalBytes)/1e6,
				p.SpeedBytesPerS/1e6,
				chunksStr,
			)
		}
	}()

	start := time.Now()
	var err error
	switch {
	case *adaptive:
		err = e.DownloadAdaptive(ctx, engine.AdaptiveOptions{
			URLs:        urls,
			OutputPath:  outPath,
			ExpectedSHA: *sha,
			MinWorkers:  *minWorkers,
			MaxWorkers:  *maxWorkers,
			TrackerURL:  *tracker,
			ListenAddr:  *listen,
			AuthToken:   *token,
		})
	case *queueMode:
		err = e.DownloadQueue(ctx, engine.QueueOptions{
			URLs:        urls,
			OutputPath:  outPath,
			Workers:     *conns,
			PieceSize:   int64(*pieceSizeMB) * 1024 * 1024,
			ExpectedSHA: *sha,
			TrackerURL:  *tracker,
			ListenAddr:  *listen,
			AuthToken:   *token,
		})
	default:
		err = e.Download(ctx)
	}
	if err != nil {
		fmt.Printf("\ndownload failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\ndone in %s -> %s\n", time.Since(start).Round(time.Millisecond), outPath)
}
