// Package dl2 is the public SDK for the Downloading 2.0 engine. Import this
// from other Go projects to get multi-connection, multi-source, and
// peer-assisted (swarm) downloading without depending on internal packages.
//
//	import "github.com/ibimo-o/Downloading-2.0/pkg/dl2"
//
//	err := dl2.Download(ctx, dl2.Options{
//	    URL:    "https://example.com/file.zip",
//	    Output: "file.zip",
//	})
package dl2

import (
	"context"

	"github.com/ibimo-o/Downloading-2.0/internal/engine"
)

// Options configures a download. This mirrors engine.QueueOptions but is
// the stable public surface -- internal/engine is free to change shape
// without breaking SDK consumers, as long as this wrapper is updated to
// match.
type Options struct {
	URL         string   // primary source URL (required)
	Mirrors     []string // optional additional mirror URLs for the same file
	Output      string   // output file path (required)
	Connections int      // parallel connections/workers, default 16
	PieceMB     int      // piece size in MB for the work-queue, default 2
	ExpectedSHA string   // optional SHA-256 to verify the final file against

	// Swarm mode (Phase 4). Leave TrackerURL empty to disable.
	TrackerURL string
	ListenAddr string // this instance's own piece-server address, e.g. "localhost:9091"
	AuthToken  string // optional shared-secret token if the tracker/server requires one (see SECURITY.md)

	// Phase 6: adaptive mode (dynamic worker scaling, adaptive piece
	// sizing, straggler redundant-fetch, latency-aware source scoring).
	MinWorkers int // adaptive mode starting/minimum concurrency, default 4
	MaxWorkers int // adaptive mode ceiling, default 64
}

// DownloadAdaptive runs the Phase 6 adaptive engine directly: dynamic
// worker scaling, adaptive piece sizing, straggler redundant-fetch, and
// latency-aware source scoring, instead of the fixed work-queue engine
// used by Download/DownloadWithProgress.
func DownloadAdaptive(ctx context.Context, opts Options) error {
	urls := append([]string{opts.URL}, opts.Mirrors...)
	e := engine.New(engine.Options{URLs: urls})
	return e.DownloadAdaptive(ctx, engine.AdaptiveOptions{
		URLs:        urls,
		OutputPath:  opts.Output,
		ExpectedSHA: opts.ExpectedSHA,
		MinWorkers:  opts.MinWorkers,
		MaxWorkers:  opts.MaxWorkers,
		TrackerURL:  opts.TrackerURL,
		ListenAddr:  opts.ListenAddr,
		AuthToken:   opts.AuthToken,
	})
}

// Progress reports live download progress. Matches engine.Progress.
type Progress = engine.Progress

// Download runs a full download to completion using the dynamic work-queue
// engine (Phase 2/3/4 combined). If you need live progress updates, use
// DownloadWithProgress instead.
func Download(ctx context.Context, opts Options) error {
	return DownloadWithProgress(ctx, opts, nil)
}

// DownloadWithProgress is like Download but also streams progress updates
// to the given channel as the download proceeds, if non-nil. The channel is
// closed by the engine when the download finishes (success or failure).
func DownloadWithProgress(ctx context.Context, opts Options, onProgress chan<- Progress) error {
	urls := append([]string{opts.URL}, opts.Mirrors...)

	pieceMB := opts.PieceMB
	if pieceMB <= 0 {
		pieceMB = 2
	}
	conns := opts.Connections
	if conns <= 0 {
		conns = 16
	}

	e := engine.New(engine.Options{URLs: urls})

	if onProgress != nil {
		go func() {
			for p := range e.Progress() {
				onProgress <- p
			}
			close(onProgress)
		}()
	}

	return e.DownloadQueue(ctx, engine.QueueOptions{
		URLs:        urls,
		OutputPath:  opts.Output,
		Workers:     conns,
		PieceSize:   int64(pieceMB) * 1024 * 1024,
		ExpectedSHA: opts.ExpectedSHA,
		TrackerURL:  opts.TrackerURL,
		ListenAddr:  opts.ListenAddr,
		AuthToken:   opts.AuthToken,
	})
}
