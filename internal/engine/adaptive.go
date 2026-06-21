// Phase 7: the unified adaptive + swarm engine. This replaces the earlier
// split between "-adaptive" (Phase 6) and "-queue -tracker" (Phase 4
// swarm) with one engine that does both together.
//
// The technical problem that caused the original split: swarm mode
// requires every peer to agree on identical piece boundaries -- "piece
// #7" has to mean the same byte range for every downloader, or peers
// can't match and verify each other's pieces. Phase 6's lazy,
// per-source-sized pieces meant two adaptive downloaders would split the
// same file completely differently, making peer matching meaningless.
//
// The fix: a fixed, shared "grid" of small pieces (GridPieceSize, default
// 1MB) that every downloader and peer agrees on. Adaptive workers claim
// multiple grid pieces at once in a single batched HTTP request, sized by
// measured throughput (fast sources grab big batches, slow ones grab one
// grid piece at a time). Batches are split back into individual grid
// pieces after fetching, each hashed and stored separately -- fully
// swarm-compatible while keeping adaptive sizing's efficiency.
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

// AdaptiveOptions configures the unified Phase 7 engine.
type AdaptiveOptions struct {
	URLs        []string
	OutputPath  string
	TempDir     string
	ExpectedSHA string

	MinWorkers    int
	MaxWorkers    int
	GridPieceSize int64
	MaxBatchSize  int64
	ScaleInterval time.Duration

	TrackerURL string
	ListenAddr string
	AuthToken  string
}

func (o *AdaptiveOptions) setDefaults() {
	if o.MinWorkers <= 0 {
		o.MinWorkers = 4
	}
	if o.MaxWorkers <= 0 {
		o.MaxWorkers = 64
	}
	if o.GridPieceSize <= 0 {
		o.GridPieceSize = 1 * 1024 * 1024
	}
	if o.MaxBatchSize <= 0 {
		o.MaxBatchSize = 16 * 1024 * 1024
	}
	if o.ScaleInterval <= 0 {
		o.ScaleInterval = 2 * time.Second
	}
}

type adaptiveSource struct {
	url            string
	bytesMoved     int64
	requestCount   int64
	totalLatencyMs int64
	activeWorkers  int32
	errors         int32
}

func (s *adaptiveSource) avgLatencyMs() float64 {
	rc := atomic.LoadInt64(&s.requestCount)
	if rc == 0 {
		return 50
	}
	return float64(atomic.LoadInt64(&s.totalLatencyMs)) / float64(rc)
}

func (s *adaptiveSource) score() float64 {
	bytes := float64(atomic.LoadInt64(&s.bytesMoved) + 1)
	errPenalty := 1.0 / float64(1+atomic.LoadInt32(&s.errors))
	loadPenalty := 1.0 / float64(1+atomic.LoadInt32(&s.activeWorkers))
	latencyPenalty := 100.0 / (100.0 + s.avgLatencyMs())
	return bytes * errPenalty * loadPenalty * latencyPenalty
}

func (s *adaptiveSource) nextBatchGridCount(gridSize, maxBatch int64) int {
	rc := atomic.LoadInt64(&s.requestCount)
	if rc < 2 {
		return 1
	}
	avgBytesPerReq := atomic.LoadInt64(&s.bytesMoved) / rc
	if avgBytesPerReq > maxBatch {
		avgBytesPerReq = maxBatch
	}
	count := int(avgBytesPerReq / gridSize)
	if count < 1 {
		count = 1
	}
	return count
}

// DownloadAdaptive runs the unified Phase 7 engine.
func (e *Engine) DownloadAdaptive(ctx context.Context, opts AdaptiveOptions) error {
	opts.setDefaults()
	if len(opts.URLs) == 0 {
		return fmt.Errorf("no source URLs provided")
	}
	if opts.TempDir == "" {
		opts.TempDir = os.TempDir()
	}

	totalSize, ranged, err := e.probeSize(ctx, opts.URLs[0])
	if err != nil {
		return fmt.Errorf("probing file size failed: %w", err)
	}
	if !ranged || totalSize <= 0 {
		opts.MaxWorkers = 1
		opts.MinWorkers = 1
	}

	const smallFileThreshold = 4 * 1024 * 1024
	if totalSize > 0 && totalSize < smallFileThreshold {
		return e.downloadSmallFile(ctx, opts.URLs[0], opts.OutputPath, opts.ExpectedSHA, totalSize)
	}

	jobDir := filepath.Join(opts.TempDir, fmt.Sprintf("dl2-adaptive-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(jobDir)

	grid := chunk.PlanQueue(totalSize, opts.GridPieceSize)
	totalGrid := len(grid)

	pieceStore, err := swarm.NewStore(filepath.Join(jobDir, "pieces"))
	if err != nil {
		return fmt.Errorf("creating piece store: %w", err)
	}
	defer pieceStore.Close()

	var swarmClient *swarm.Client
	swarmEnabled := opts.TrackerURL != "" && opts.ListenAddr != ""
	var cachedPeers []swarm.PeerInfo
	var peersMu sync.RWMutex

	if swarmEnabled {
		fh := sha256.Sum256([]byte(opts.URLs[0]))
		fileHash := fmt.Sprintf("%x", fh)
		swarmClient = swarm.NewClient(opts.TrackerURL, fileHash, opts.ListenAddr, opts.AuthToken, pieceStore)
		if err := swarm.StartPeerServer(opts.ListenAddr, pieceStore); err != nil {
			return fmt.Errorf("starting peer server: %w", err)
		}
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
		go func() {
			ticker := time.NewTicker(750 * time.Millisecond)
			defer ticker.Stop()
			refresh := func() {
				if peers, perr := swarmClient.Peers(); perr == nil {
					peersMu.Lock()
					cachedPeers = peers
					peersMu.Unlock()
				}
			}
			refresh()
			for {
				select {
				case <-announceCtx.Done():
					return
				case <-ticker.C:
					refresh()
				}
			}
		}()
	}

	sources := make([]*adaptiveSource, len(opts.URLs))
	for i, u := range opts.URLs {
		sources[i] = &adaptiveSource{url: u}
	}
	pickSource := func() *adaptiveSource {
		best := sources[0]
		bestScore := -1.0
		for _, s := range sources {
			sc := s.score()
			if sc > bestScore {
				bestScore = sc
				best = s
			}
		}
		return best
	}

	var cursor int
	var cursorMu sync.Mutex

	var downloaded int64
	var piecesDone int32
	var activeWorkers int32
	var desiredWorkers int32 = int32(opts.MinWorkers)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	stopCh := make(chan struct{})
	var stopOnce sync.Once

	type inFlight struct {
		startedAt time.Time
		size      int64
	}
	var inFlightMu sync.Mutex
	inFlightBatches := make(map[int]inFlight)
	stragglerRetried := make(map[int]bool)

	fail := func(ferr error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = ferr
		}
		errMu.Unlock()
		stopOnce.Do(func() { close(stopCh) })
	}

	spawnWorker := func() {
		atomic.AddInt32(&activeWorkers, 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer atomic.AddInt32(&activeWorkers, -1)
			for {
				select {
				case <-stopCh:
					return
				case <-ctx.Done():
					return
				default:
				}
				if atomic.LoadInt32(&activeWorkers) > atomic.LoadInt32(&desiredWorkers) {
					return
				}

				src := pickSource()

				if swarmEnabled {
					cursorMu.Lock()
					idx := cursor
					isDone := idx >= totalGrid
					cursorMu.Unlock()
					if isDone {
						return
					}
					peersMu.RLock()
					peerAddr, expectedHash := swarm.FindPeerWithPiece(cachedPeers, idx)
					peersMu.RUnlock()

					if peerAddr != "" {
						claimed := false
						cursorMu.Lock()
						if cursor == idx {
							cursor++
							claimed = true
						}
						cursorMu.Unlock()

						if claimed {
							inFlightMu.Lock()
							inFlightBatches[idx] = inFlight{startedAt: time.Now(), size: grid[idx].Size()}
							inFlightMu.Unlock()

							data, ferr := swarmClient.FetchFromPeer(peerAddr, idx, expectedHash)
							if ferr == nil {
								if perr := pieceStore.Put(idx, data); perr == nil {
									atomic.AddInt64(&downloaded, int64(len(data)))
									atomic.AddInt32(&piecesDone, 1)
									swarmClient.AnnouncePiece()
									inFlightMu.Lock()
									delete(inFlightBatches, idx)
									inFlightMu.Unlock()
									continue
								}
							}
							if berr := fetchAndStoreBatch(ctx, e, src, grid, idx, idx, pieceStore, swarmEnabled, swarmClient, &downloaded, &piecesDone); berr != nil {
								atomic.AddInt32(&src.errors, 1)
								fail(fmt.Errorf("grid piece %d failed (source %s): %w", idx, src.url, berr))
								inFlightMu.Lock()
								delete(inFlightBatches, idx)
								inFlightMu.Unlock()
								return
							}
							inFlightMu.Lock()
							delete(inFlightBatches, idx)
							inFlightMu.Unlock()
							continue
						}
						continue
					}
				}

				cursorMu.Lock()
				if cursor >= totalGrid {
					cursorMu.Unlock()
					return
				}
				startIdx := cursor
				count := src.nextBatchGridCount(opts.GridPieceSize, opts.MaxBatchSize)
				endIdx := startIdx + count - 1
				if endIdx >= totalGrid {
					endIdx = totalGrid - 1
				}
				cursor = endIdx + 1
				cursorMu.Unlock()

				var batchSize int64
				for i := startIdx; i <= endIdx; i++ {
					batchSize += grid[i].Size()
				}
				inFlightMu.Lock()
				inFlightBatches[startIdx] = inFlight{startedAt: time.Now(), size: batchSize}
				inFlightMu.Unlock()

				atomic.AddInt32(&src.activeWorkers, 1)
				berr := fetchAndStoreBatch(ctx, e, src, grid, startIdx, endIdx, pieceStore, swarmEnabled, swarmClient, &downloaded, &piecesDone)
				atomic.AddInt32(&src.activeWorkers, -1)

				inFlightMu.Lock()
				delete(inFlightBatches, startIdx)
				inFlightMu.Unlock()

				if berr != nil {
					atomic.AddInt32(&src.errors, 1)
					fail(fmt.Errorf("batch [%d-%d] failed (source %s): %w", startIdx, endIdx, src.url, berr))
					return
				}
			}
		}()
	}

	for i := 0; i < opts.MinWorkers; i++ {
		spawnWorker()
	}

	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	go func() {
		ticker := time.NewTicker(opts.ScaleInterval)
		defer ticker.Stop()
		var lastBytes int64
		var lastThroughput float64

		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				cur := atomic.LoadInt64(&downloaded)
				delta := cur - lastBytes
				throughput := float64(delta) / opts.ScaleInterval.Seconds()
				lastBytes = cur

				if lastThroughput > 0 {
					improvement := throughput / lastThroughput
					active := atomic.LoadInt32(&activeWorkers)
					switch {
					case improvement > 1.10 && active < int32(opts.MaxWorkers):
						grow := active / 2
						if grow < 1 {
							grow = 1
						}
						newDesired := active + grow
						if newDesired > int32(opts.MaxWorkers) {
							newDesired = int32(opts.MaxWorkers)
						}
						atomic.StoreInt32(&desiredWorkers, newDesired)
						for active < newDesired {
							spawnWorker()
							active++
						}
					case improvement < 0.90 && active > int32(opts.MinWorkers):
						shrink := active / 4
						if shrink < 1 {
							shrink = 1
						}
						newDesired := active - shrink
						if newDesired < int32(opts.MinWorkers) {
							newDesired = int32(opts.MinWorkers)
						}
						atomic.StoreInt32(&desiredWorkers, newDesired)
					}
				}
				lastThroughput = throughput

				if throughput > 0 {
					inFlightMu.Lock()
					for idx, fl := range inFlightBatches {
						expected := time.Duration(float64(fl.size)/throughput*float64(time.Second)) * 3
						if expected < 2*time.Second {
							expected = 2 * time.Second
						}
						if time.Since(fl.startedAt) > expected && !stragglerRetried[idx] {
							stragglerRetried[idx] = true
							active := atomic.LoadInt32(&activeWorkers)
							if active < int32(opts.MaxWorkers) {
								atomic.StoreInt32(&desiredWorkers, active+1)
								spawnWorker()
							}
						}
					}
					inFlightMu.Unlock()
				}

				select {
				case e.progress <- Progress{
					TotalBytes:      totalSize,
					DownloadedBytes: cur,
					SpeedBytesPerS:  throughput,
					ChunksDone:      int(atomic.LoadInt32(&piecesDone)),
					ChunksTotal:     totalGrid,
				}:
				default:
				}
			}
		}
	}()

	wg.Wait()
	cancelMonitor()

	if firstErr != nil {
		return firstErr
	}

	out, err := os.Create(opts.OutputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer out.Close()
	for i := 0; i < totalGrid; i++ {
		data, ok := pieceStore.Get(i)
		if !ok {
			return fmt.Errorf("missing grid piece %d during reassembly", i)
		}
		if _, werr := out.Write(data); werr != nil {
			return fmt.Errorf("writing grid piece %d: %w", i, werr)
		}
	}

	if opts.ExpectedSHA != "" {
		if err := chunk.VerifyWholeFile(opts.OutputPath, opts.ExpectedSHA); err != nil {
			return fmt.Errorf("integrity check failed: %w", err)
		}
	}

	// Flush a final, true 100% progress update -- the periodic monitor
	// ticks every ScaleInterval, so the last update printed during the
	// loop can lag a few seconds (and a few percent) behind actual
	// completion. The download itself was already fully correct and
	// verified by this point; this just makes the live display match.
	select {
	case e.progress <- Progress{
		TotalBytes:      totalSize,
		DownloadedBytes: totalSize,
		SpeedBytesPerS:  0,
		ChunksDone:      totalGrid,
		ChunksTotal:     totalGrid,
	}:
	default:
	}

	close(e.progress)
	return nil
}

// fetchAndStoreBatch fetches grid pieces [startIdx, endIdx] in one ranged
// HTTP request, splits the response into individual grid pieces, stores
// each in pieceStore (hashed), and announces them to the swarm if enabled.
func fetchAndStoreBatch(ctx context.Context, e *Engine, src *adaptiveSource, grid []*chunk.Chunk, startIdx, endIdx int, pieceStore *swarm.Store, swarmEnabled bool, swarmClient *swarm.Client, downloaded *int64, piecesDone *int32) error {
	start := grid[startIdx].Start
	end := grid[endIdx].End

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	reqStart := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	ttfb := time.Since(reqStart).Milliseconds()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var offset int64
	for i := startIdx; i <= endIdx; i++ {
		size := grid[i].Size()
		if offset+size > int64(len(body)) {
			return fmt.Errorf("batch response shorter than expected (grid piece %d)", i)
		}
		piece := body[offset : offset+size]
		if perr := pieceStore.Put(i, piece); perr != nil {
			return fmt.Errorf("storing grid piece %d: %w", i, perr)
		}
		offset += size
		atomic.AddInt32(piecesDone, 1)
		if swarmEnabled {
			swarmClient.AnnouncePiece()
		}
	}

	atomic.AddInt64(&src.bytesMoved, int64(len(body)))
	atomic.AddInt64(&src.requestCount, 1)
	atomic.AddInt64(&src.totalLatencyMs, ttfb)
	atomic.AddInt64(downloaded, int64(len(body)))
	return nil
}
