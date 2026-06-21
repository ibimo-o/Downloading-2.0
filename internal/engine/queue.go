package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ibimo-o/Downloading-2.0/internal/chunk"
	"github.com/ibimo-o/Downloading-2.0/internal/swarm"
)

// sourceStats tracks live performance for one mirror/source URL, so the
// scheduler can bias future piece assignment toward whichever source is
// actually fastest right now (not just round-robin).
type sourceStats struct {
	url           string
	bytesMoved    int64
	activeWorkers int32
	totalErrors   int32
}

func (s *sourceStats) speedScore() float64 {
	// Simple score: bytes moved so far, penalized by errors and by how many
	// workers are already hammering this source (avoid piling everything
	// onto one mirror even if it looked fast early on).
	errPenalty := 1.0 / float64(1+atomic.LoadInt32(&s.totalErrors))
	loadPenalty := 1.0 / float64(1+atomic.LoadInt32(&s.activeWorkers))
	return float64(atomic.LoadInt64(&s.bytesMoved)+1) * errPenalty * loadPenalty
}

// QueueOptions configures the Phase 2 dynamic work-queue download.
type QueueOptions struct {
	URLs        []string
	OutputPath  string
	Workers     int   // number of concurrent worker goroutines pulling from the queue
	PieceSize   int64 // size of each work-queue piece in bytes (smaller = more adaptive, more overhead)
	TempDir     string
	ExpectedSHA string
	MaxRetries  int

	// Phase 4 swarm options. Leave TrackerURL empty to disable swarm mode
	// entirely (engine behaves exactly like Phase 2).
	TrackerURL string // e.g. http://localhost:9090
	ListenAddr string // this instance's own piece-server address, e.g. localhost:9091
	AuthToken  string // optional shared-secret if the tracker requires one
}

// DownloadQueue runs Phase 2: many small pieces in a shared queue, pulled by
// a fixed pool of workers, with chunk-to-source assignment biased toward
// whichever mirror is currently fastest. This avoids the Phase 1 problem of
// a single slow fixed chunk stalling the entire job's tail end.
func (e *Engine) DownloadQueue(ctx context.Context, qo QueueOptions) error {
	if len(qo.URLs) == 0 {
		return fmt.Errorf("no source URLs provided")
	}
	if qo.Workers <= 0 {
		qo.Workers = 16
	}
	if qo.MaxRetries <= 0 {
		qo.MaxRetries = 3
	}
	if qo.TempDir == "" {
		qo.TempDir = os.TempDir()
	}

	totalSize, ranged, err := e.probeSize(ctx, qo.URLs[0])
	if err != nil {
		return fmt.Errorf("probing file size failed: %w", err)
	}
	if !ranged || totalSize <= 0 {
		// fall back to a single full-file piece, single worker
		qo.Workers = 1
	}

	// Small-file fast path: below this size, multi-connection chunking
	// adds pure overhead (extra HTTP round trips, more goroutines, more
	// scheduling) without enough data to actually parallelize
	// meaningfully. A plain single GET is faster and simpler for tiny
	// files -- this matters for "infrastructure" use where dl2 might be
	// asked to fetch files of any size, not just large test files.
	const smallFileThreshold = 4 * 1024 * 1024 // 4MB
	if totalSize > 0 && totalSize < smallFileThreshold {
		return e.downloadSmallFile(ctx, qo.URLs[0], qo.OutputPath, qo.ExpectedSHA, totalSize)
	}

	pieces := chunk.PlanQueue(totalSize, qo.PieceSize)

	jobDir := filepath.Join(qo.TempDir, fmt.Sprintf("dl2-q-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(jobDir)

	// Phase 4: swarm setup. fileHash identifies "this file" to the tracker
	// and other peers -- derived from the primary URL since we don't know
	// file content hash ahead of time without downloading it first.
	var swarmClient *swarm.Client
	var swarmStore *swarm.Store
	swarmEnabled := qo.TrackerURL != "" && qo.ListenAddr != ""
	if swarmEnabled {
		fileHash := fmt.Sprintf("%x", sha256.Sum256([]byte(qo.URLs[0])))
		var serr error
		swarmStore, serr = swarm.NewStore(filepath.Join(jobDir, "swarm-cache"))
		if serr != nil {
			return fmt.Errorf("creating swarm piece store: %w", serr)
		}
		defer swarmStore.Close()
		swarmClient = swarm.NewClient(qo.TrackerURL, fileHash, qo.ListenAddr, qo.AuthToken, swarmStore)
		if err := swarm.StartPeerServer(qo.ListenAddr, swarmStore); err != nil {
			return fmt.Errorf("starting peer server: %w", err)
		}
		// Announce periodically so peers (and we) stay discoverable.
		announceCtx, cancelAnnounce := context.WithCancel(ctx)
		defer cancelAnnounce()
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			swarmClient.Announce()
			for {
				select {
				case <-announceCtx.Done():
					return
				case <-ticker.C:
					swarmClient.Announce()
				}
			}
		}()
	}

	stats := make([]*sourceStats, len(qo.URLs))
	for i, u := range qo.URLs {
		stats[i] = &sourceStats{url: u}
	}

	// pick chooses the best-scoring source right now.
	pick := func() *sourceStats {
		best := stats[0]
		bestScore := -1.0
		for _, s := range stats {
			sc := s.speedScore()
			if sc > bestScore {
				bestScore = sc
				best = s
			}
		}
		return best
	}

	work := make(chan *chunk.Chunk, len(pieces))
	for _, p := range pieces {
		work <- p
	}
	close(work)

	var downloaded int64
	var doneCount int32
	startTime := time.Now()

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
					ChunksDone:      int(atomic.LoadInt32(&doneCount)),
					ChunksTotal:     len(pieces),
				}:
				default:
				}
			}
		}
	}()

	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	for w := 0; w < qo.Workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for p := range work {
				select {
				case <-ctx.Done():
					return
				default:
				}

				src := pick()
				p.SourceURL = src.url
				p.TempPath = filepath.Join(jobDir, fmt.Sprintf("part-%06d", p.Index))

				var err error
				gotFromPeer := false

				if swarmEnabled {
					if peers, perr := swarmClient.Peers(); perr == nil {
						if peerAddr, expectedHash := swarm.FindPeerWithPiece(peers, p.Index); peerAddr != "" {
							// Race: peer fetch vs origin fetch, whichever finishes
							// first wins. This guarantees swarm mode is never
							// slower than origin-only, and captures the full
							// speed benefit whenever a peer is faster.
							type fetchResult struct {
								from string
								data []byte
								err  error
							}
							resultCh := make(chan fetchResult, 2)
							raceCtx, cancelRace := context.WithCancel(ctx)

							go func() {
								data, ferr := swarmClient.FetchFromPeer(peerAddr, p.Index, expectedHash)
								select {
								case resultCh <- fetchResult{from: "peer", data: data, err: ferr}:
								case <-raceCtx.Done():
								}
							}()

							atomic.AddInt32(&src.activeWorkers, 1)
							go func() {
								data, ferr := e.fetchPieceBytes(raceCtx, p, src)
								select {
								case resultCh <- fetchResult{from: "origin", data: data, err: ferr}:
								case <-raceCtx.Done():
								}
								atomic.AddInt32(&src.activeWorkers, -1)
							}()

							// Wait for first successful result (or both failing).
							var winner fetchResult
							for i := 0; i < 2; i++ {
								r := <-resultCh
								if r.err == nil {
									winner = r
									break
								}
								winner = r // keep last (possibly failed) result if both fail
							}
							cancelRace()

							if winner.err == nil {
								if werr := os.WriteFile(p.TempPath, winner.data, 0o644); werr == nil {
									atomic.AddInt64(&downloaded, int64(len(winner.data)))
									swarmStore.Put(p.Index, winner.data)
									swarmClient.AnnouncePiece()
									p.Downloaded = true
									gotFromPeer = true
								}
							} else {
								err = winner.err
							}
						}
					}
				}

				if !gotFromPeer {
					atomic.AddInt32(&src.activeWorkers, 1)
					err = e.downloadPieceWithRetry(ctx, p, qo.MaxRetries, &downloaded, src)
					atomic.AddInt32(&src.activeWorkers, -1)
					if err == nil && swarmEnabled {
						if data, rerr := os.ReadFile(p.TempPath); rerr == nil {
							swarmStore.Put(p.Index, data)
							swarmClient.AnnouncePiece()
						}
					}
				}

				if err != nil {
					atomic.AddInt32(&src.totalErrors, 1)
					errMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("piece %d failed (source %s): %w", p.Index, src.url, err)
					}
					errMu.Unlock()
					continue
				}
				atomic.AddInt32(&doneCount, 1)
			}
		}(w)
	}

	wg.Wait()
	cancelTicker()

	if firstErr != nil {
		return firstErr
	}

	if err := reassemble(pieces, qo.OutputPath); err != nil {
		return fmt.Errorf("reassembly failed: %w", err)
	}

	if qo.ExpectedSHA != "" {
		if err := chunk.VerifyWholeFile(qo.OutputPath, qo.ExpectedSHA); err != nil {
			return fmt.Errorf("integrity check failed: %w", err)
		}
	}

	// Flush a final, true 100% progress update -- the periodic ticker
	// can lag a tick behind actual completion otherwise.
	select {
	case e.progress <- Progress{
		TotalBytes:      totalSize,
		DownloadedBytes: totalSize,
		ChunksDone:      len(pieces),
		ChunksTotal:     len(pieces),
	}:
	default:
	}

	close(e.progress)
	return nil
}

// fetchPieceBytes fetches a piece's bytes from the origin source, used by
// the swarm race logic. Unlike downloadPieceOnce, it returns bytes directly
// rather than writing to disk or touching the shared downloaded counter --
// the caller (race winner handling) does that once for whichever source won.
func (e *Engine) fetchPieceBytes(ctx context.Context, c *chunk.Chunk, src *sourceStats) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.SourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", c.Start, c.End))

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	atomic.AddInt64(&src.bytesMoved, int64(len(data)))
	return data, nil
}

// downloadSmallFile is the fast path for files under smallFileThreshold:
// a single plain GET, no chunking, no worker pool, no swarm overhead.
// Still reports progress and verifies the hash if requested, so callers
// see consistent behavior regardless of which path was taken.
func (e *Engine) downloadSmallFile(ctx context.Context, url, outputPath, expectedSHA string, totalSize int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil && filepath.Dir(outputPath) != "." {
		return err
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			select {
			case e.progress <- Progress{TotalBytes: totalSize, DownloadedBytes: written, ChunksDone: 1, ChunksTotal: 1}:
			default:
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}

	if expectedSHA != "" {
		if err := chunk.VerifyWholeFile(outputPath, expectedSHA); err != nil {
			return fmt.Errorf("integrity check failed: %w", err)
		}
	}

	close(e.progress)
	return nil
}

func (e *Engine) downloadPieceWithRetry(ctx context.Context, c *chunk.Chunk, maxRetries int, downloaded *int64, src *sourceStats) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
		}
		if err := e.downloadPieceOnce(ctx, c, downloaded, src); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func (e *Engine) downloadPieceOnce(ctx context.Context, c *chunk.Chunk, downloaded *int64, src *sourceStats) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.SourceURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", c.Start, c.End))

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
			atomic.AddInt64(&src.bytesMoved, int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}

	c.Downloaded = true
	return nil
}
