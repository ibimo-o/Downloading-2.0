package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ibimo-o/Downloading-2.0/internal/chunk"
)

// Options configures a download job.
type Options struct {
	URLs        []string // one or more sources for the same file (mirrors). First is primary.
	OutputPath  string   // final file path
	Connections int      // number of parallel chunks/connections (e.g. 8, 16, 32)
	TempDir     string   // where chunk pieces are staged
	ExpectedSHA string   // optional whole-file hash to verify after reassembly
	MaxRetries  int      // retries per chunk on failure
}

// Progress is sent on the Progress channel as the download proceeds.
type Progress struct {
	TotalBytes      int64
	DownloadedBytes int64
	SpeedBytesPerS  float64
	ChunksDone      int
	ChunksTotal     int
}

type Engine struct {
	opts     Options
	client   *http.Client
	progress chan Progress
}

func New(opts Options) *Engine {
	if opts.Connections <= 0 {
		opts.Connections = 8
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 3
	}
	if opts.TempDir == "" {
		opts.TempDir = os.TempDir()
	}
	return &Engine{
		opts: opts,
		client: &http.Client{
			Timeout: 0, // per-request timeouts handled via context below
		},
		progress: make(chan Progress, 16),
	}
}

// Progress exposes the channel callers can listen on for live updates.
func (e *Engine) Progress() <-chan Progress {
	return e.progress
}

// probeSize does a HEAD request to find total file size and whether the
// server supports byte-range requests (required for chunked parallel download).
func (e *Engine) probeSize(ctx context.Context, url string) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()

	acceptsRanges := resp.Header.Get("Accept-Ranges") == "bytes"
	return resp.ContentLength, acceptsRanges, nil
}

// Download runs the full multi-connection chunked download and reassembles the file.
func (e *Engine) Download(ctx context.Context) error {
	if len(e.opts.URLs) == 0 {
		return fmt.Errorf("no source URLs provided")
	}

	totalSize, ranged, err := e.probeSize(ctx, e.opts.URLs[0])
	if err != nil {
		return fmt.Errorf("probing file size failed: %w", err)
	}

	numConns := e.opts.Connections
	if !ranged || totalSize <= 0 {
		// Server doesn't support range requests — fall back to single connection.
		numConns = 1
	}

	chunks := chunk.Plan(totalSize, numConns, e.opts.URLs)

	jobDir := filepath.Join(e.opts.TempDir, fmt.Sprintf("dl2-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(jobDir)

	var downloaded int64
	var done int32
	startTime := time.Now()

	// progress reporter ticker
	tickerCtx, cancelTicker := context.WithCancel(ctx)
	defer cancelTicker()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-tickerCtx.Done():
				return
			case <-ticker.C:
				elapsed := time.Since(startTime).Seconds()
				d := atomic.LoadInt64(&downloaded)
				speed := 0.0
				if elapsed > 0 {
					speed = float64(d) / elapsed
				}
				select {
				case e.progress <- Progress{
					TotalBytes:      totalSize,
					DownloadedBytes: d,
					SpeedBytesPerS:  speed,
					ChunksDone:      int(atomic.LoadInt32(&done)),
					ChunksTotal:     len(chunks),
				}:
				default:
				}
			}
		}
	}()

	g, gctx := errGroup(ctx)
	sem := make(chan struct{}, numConns)

	for _, c := range chunks {
		c := c
		sem <- struct{}{}
		g.Go(func() error {
			defer func() { <-sem }()
			c.TempPath = filepath.Join(jobDir, fmt.Sprintf("part-%05d", c.Index))
			if err := e.downloadChunkWithRetry(gctx, c, &downloaded); err != nil {
				return fmt.Errorf("chunk %d failed: %w", c.Index, err)
			}
			atomic.AddInt32(&done, 1)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	if err := reassemble(chunks, e.opts.OutputPath); err != nil {
		return fmt.Errorf("reassembly failed: %w", err)
	}

	if e.opts.ExpectedSHA != "" {
		if err := chunk.VerifyWholeFile(e.opts.OutputPath, e.opts.ExpectedSHA); err != nil {
			return fmt.Errorf("integrity check failed: %w", err)
		}
	}

	close(e.progress)
	return nil
}

func (e *Engine) downloadChunkWithRetry(ctx context.Context, c *chunk.Chunk, downloaded *int64) error {
	var lastErr error
	for attempt := 0; attempt <= e.opts.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond) // simple backoff
		}
		if err := e.downloadChunkOnce(ctx, c, downloaded); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func (e *Engine) downloadChunkOnce(ctx context.Context, c *chunk.Chunk, downloaded *int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.SourceURL, nil)
	if err != nil {
		return err
	}
	if c.Size() > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", c.Start, c.End))
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	out, err := os.Create(c.TempPath)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			atomic.AddInt64(downloaded, int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}

	if err := chunk.VerifySHA256(c.TempPath, c.SHA256); err != nil {
		return err
	}

	c.Downloaded = true
	return nil
}

// reassemble concatenates all chunk files in order into the final output path.
func reassemble(chunks []*chunk.Chunk, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil && filepath.Dir(outPath) != "." {
		return err
	}
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	for _, c := range chunks {
		in, err := os.Open(c.TempPath)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			return err
		}
		in.Close()
	}
	return nil
}

// --- tiny inline errgroup so we don't need an external dependency ---

type group struct {
	wg      sync.WaitGroup
	errOnce sync.Once
	err     error
	ctx     context.Context
	cancel  context.CancelFunc
}

func errGroup(ctx context.Context) (*group, context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	return &group{ctx: cctx, cancel: cancel}, cctx
}

func (g *group) Go(f func() error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := f(); err != nil {
			g.errOnce.Do(func() {
				g.err = err
				g.cancel()
			})
		}
	}()
}

func (g *group) Wait() error {
	g.wg.Wait()
	g.cancel()
	return g.err
}
