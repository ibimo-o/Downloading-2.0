// Command swarmbench measures the real benefit of Phase 4 swarm mode in a
// controlled way: for N trials, it downloads the same file twice
// sequentially -- "copy1" with no peers available (origin only, since it's
// the first downloader) and "copy2" immediately after (which can pull
// pieces from copy1 via the swarm). A small unique query string is added to
// the URL per trial so each trial starts with a clean swarm namespace --
// otherwise leftover peer state from a previous trial would let copy1
// unfairly benefit too, hiding the real copy1-vs-copy2 gap.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/ibimo-o/Downloading-2.0/internal/engine"
)

// --- minimal in-process tracker (same logic as cmd/tracker, duplicated
// here so this binary is self-contained and doesn't need a separate
// tracker process running) ---

type pieceHash struct {
	Index int    `json:"index"`
	Hash  string `json:"hash"`
}

type peerInfo struct {
	Addr     string
	Pieces   []pieceHash
	LastSeen time.Time
}

type miniTracker struct {
	mu    sync.RWMutex
	swarm map[string]map[string]*peerInfo
}

func newMiniTracker() *miniTracker {
	return &miniTracker{swarm: make(map[string]map[string]*peerInfo)}
}

func (t *miniTracker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/announce", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FileHash string `json:"file_hash"`
			Addr     string `json:"addr"`
			Pieces   []pieceHash `json:"pieces"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		t.mu.Lock()
		if t.swarm[req.FileHash] == nil {
			t.swarm[req.FileHash] = make(map[string]*peerInfo)
		}
		t.swarm[req.FileHash][req.Addr] = &peerInfo{Addr: req.Addr, Pieces: req.Pieces, LastSeen: time.Now()}
		t.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/peers", func(w http.ResponseWriter, r *http.Request) {
		fileHash := r.URL.Query().Get("file_hash")
		exclude := r.URL.Query().Get("exclude")
		t.mu.RLock()
		var peers []peerInfo
		cutoff := time.Now().Add(-2 * time.Minute)
		for addr, p := range t.swarm[fileHash] {
			if addr == exclude || p.LastSeen.Before(cutoff) {
				continue
			}
			peers = append(peers, *p)
		}
		t.mu.RUnlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"peers": peers})
	})
	return mux
}

// --- benchmark logic ---

const trials = 3

func main() {
	baseURL := "http://speedtest.tele2.net/100MB.zip"
	if len(os.Args) > 1 {
		baseURL = os.Args[1]
	}

	tr := newMiniTracker()
	srv := &http.Server{Addr: "localhost:9099", Handler: tr.handler()}
	go srv.ListenAndServe()
	defer srv.Close()
	time.Sleep(300 * time.Millisecond) // let it bind

	trackerURL := "http://localhost:9099"

	fmt.Printf("Swarm benchmark: %d trials, sequential copy1 -> copy2 per trial\n\n", trials)

	type trialResult struct {
		copy1Secs    float64
		copy2Secs    float64
		improvePct   float64
	}
	var results []trialResult

	for i := 0; i < trials; i++ {
		trialURL := fmt.Sprintf("%s?trial=%d", baseURL, i)
		port1 := 9200 + i*2
		port2 := port1 + 1

		fmt.Printf("Trial %d/%d\n", i+1, trials)

		c1, err := runOne(trialURL, trackerURL, fmt.Sprintf("localhost:%d", port1), fmt.Sprintf("copy1_t%d.tmp", i))
		if err != nil {
			fmt.Printf("  copy1 failed: %v\n", err)
			continue
		}
		fmt.Printf("  copy1 (no peers yet): %.2fs\n", c1)

		// give the tracker/peer-server a moment to settle, mirrors realistic
		// "second person starts shortly after" timing
		time.Sleep(1 * time.Second)

		c2, err := runOne(trialURL, trackerURL, fmt.Sprintf("localhost:%d", port2), fmt.Sprintf("copy2_t%d.tmp", i))
		if err != nil {
			fmt.Printf("  copy2 failed: %v\n", err)
			continue
		}
		improve := (c1 - c2) / c1 * 100
		fmt.Printf("  copy2 (peer-assisted): %.2fs  (%.1f%% faster than copy1)\n\n", c2, improve)

		results = append(results, trialResult{copy1Secs: c1, copy2Secs: c2, improvePct: improve})
	}

	if len(results) == 0 {
		fmt.Println("No successful trials.")
		return
	}

	var sumImprove, sumC1, sumC2 float64
	for _, r := range results {
		sumImprove += r.improvePct
		sumC1 += r.copy1Secs
		sumC2 += r.copy2Secs
	}
	n := float64(len(results))

	improvements := make([]float64, len(results))
	for i, r := range results {
		improvements[i] = r.improvePct
	}
	sort.Float64s(improvements)

	fmt.Println("=========================================")
	fmt.Println(" SWARM BENCHMARK SUMMARY")
	fmt.Println("=========================================")
	fmt.Printf("Trials completed:        %d/%d\n", len(results), trials)
	fmt.Printf("Avg copy1 (no peers):    %.2fs\n", sumC1/n)
	fmt.Printf("Avg copy2 (peer-assist): %.2fs\n", sumC2/n)
	fmt.Printf("Avg speedup from swarm:  %.1f%%\n", sumImprove/n)
	fmt.Printf("Median speedup:          %.1f%%\n", improvements[len(improvements)/2])
	fmt.Println("=========================================")

	for i := 0; i < trials; i++ {
		os.Remove(fmt.Sprintf("copy1_t%d.tmp", i))
		os.Remove(fmt.Sprintf("copy2_t%d.tmp", i))
	}
}

func runOne(url, trackerURL, listenAddr, outFile string) (float64, error) {
	e := engine.New(engine.Options{URLs: []string{url}})
	ctx := context.Background()
	start := time.Now()
	err := e.DownloadQueue(ctx, engine.QueueOptions{
		URLs:       []string{url},
		OutputPath: outFile,
		Workers:    16,
		PieceSize:  2 * 1024 * 1024,
		TrackerURL: trackerURL,
		ListenAddr: listenAddr,
	})
	if err != nil {
		return 0, err
	}
	return time.Since(start).Seconds(), nil
}
