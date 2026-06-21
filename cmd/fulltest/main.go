// Command fulltest exercises every dl2 download mode (baseline,
// fixed-queue, adaptive, and swarm) against a real range of file sizes
// (tiny -> large), and prints one consolidated report. This is the test
// to run before claiming "dl2 handles any file size with any mode."
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ibimo-o/Downloading-2.0/internal/engine"
)

type testFile struct {
	label string
	url   string
}

var testFiles = []testFile{
	{"tiny (~15KB, no chunking expected)", "https://www.google.com/favicon.ico"},
	{"small (1MB)", "https://proof.ovh.net/files/1Mb.dat"},
	{"medium (10MB)", "https://proof.ovh.net/files/10Mb.dat"},
	{"large (100MB)", "http://speedtest.tele2.net/100MB.zip"},
	{"huge (1GB)", "http://speedtest.tele2.net/1GB.zip"},
}

type runResult struct {
	mode    string
	seconds float64
	err     error
}

func main() {
	fmt.Println("=================================================================")
	fmt.Println(" DL2 FULL TEST: every mode, tiny -> large files")
	fmt.Println("=================================================================")
	fmt.Println()

	var allResults []struct {
		fileLabel string
		results   []runResult
	}

	for _, tf := range testFiles {
		fmt.Printf("--- %s ---\n", tf.label)
		fmt.Printf("    %s\n\n", tf.url)

		var results []runResult

		// 1. Baseline
		fmt.Print("  baseline (single connection)... ")
		t, err := runBaseline(tf.url, "ft_baseline.tmp")
		results = append(results, runResult{"baseline", t, err})
		printOutcome(t, err)

		// 2. Fixed work-queue (Phase 2/3)
		fmt.Print("  queue mode (16 connections)...  ")
		t, err = runQueue(tf.url, "ft_queue.tmp")
		results = append(results, runResult{"queue", t, err})
		printOutcome(t, err)

		// 3. Adaptive (Phase 6)
		fmt.Print("  adaptive mode...                ")
		t, err = runAdaptive(tf.url, "ft_adaptive.tmp")
		results = append(results, runResult{"adaptive", t, err})
		printOutcome(t, err)

		fmt.Println()
		allResults = append(allResults, struct {
			fileLabel string
			results   []runResult
		}{tf.label, results})

		cleanupTmp()
	}

	// 4. Swarm mode (queue engine) -- run against the 1GB file
	// specifically, since this is the size that actually exercises the
	// disk-backed piece store fix (a 100MB file wouldn't have shown a
	// problem even with the old in-memory store).
	fmt.Println("--- swarm mode, queue engine (1GB file, copy1 -> copy2) ---")
	c1, c2, err := runSwarmPair("http://speedtest.tele2.net/1GB.zip", false)
	if err != nil {
		fmt.Printf("  failed: %v\n\n", err)
	} else {
		improve := (c1 - c2) / c1 * 100
		fmt.Printf("  copy1 (no peers):      %.2fs\n", c1)
		fmt.Printf("  copy2 (peer-assisted): %.2fs  (%.1f%% faster)\n\n", c2, improve)
	}

	// 5. Swarm mode (Phase 7 unified adaptive+swarm engine) -- 100MB file,
	// the merge that resolved the original adaptive/swarm split.
	fmt.Println("--- swarm mode, adaptive engine / Phase 7 (100MB file, copy1 -> copy2) ---")
	a1, a2, aerr := runSwarmPair("http://speedtest.tele2.net/100MB.zip", true)
	if aerr != nil {
		fmt.Printf("  failed: %v\n\n", aerr)
	} else {
		improve := (a1 - a2) / a1 * 100
		fmt.Printf("  copy1 (no peers):      %.2fs\n", a1)
		fmt.Printf("  copy2 (peer-assisted): %.2fs  (%.1f%% faster)\n\n", a2, improve)
	}

	// --- final report ---
	fmt.Println("=================================================================")
	fmt.Println(" SUMMARY")
	fmt.Println("=================================================================")
	fmt.Printf("%-35s %-12s %10s\n", "File", "Mode", "Time(s)")
	fmt.Println("-----------------------------------------------------------------")
	for _, fr := range allResults {
		for _, r := range fr.results {
			status := fmt.Sprintf("%.2f", r.seconds)
			if r.err != nil {
				status = "FAILED"
			}
			fmt.Printf("%-35s %-12s %10s\n", fr.fileLabel, r.mode, status)
		}
	}
	if err == nil {
		fmt.Printf("%-35s %-12s %10.2f\n", "huge (swarm copy1, 1GB)", "swarm-1st", c1)
		fmt.Printf("%-35s %-12s %10.2f\n", "huge (swarm copy2, 1GB)", "swarm-2nd", c2)
	}
	if aerr == nil {
		fmt.Printf("%-35s %-12s %10.2f\n", "large (adaptive+swarm 1st, 100MB)", "phase7-1st", a1)
		fmt.Printf("%-35s %-12s %10.2f\n", "large (adaptive+swarm 2nd, 100MB)", "phase7-2nd", a2)
	}
	fmt.Println("=================================================================")
}

func printOutcome(t float64, err error) {
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
		return
	}
	fmt.Printf("%.2fs\n", t)
}

func runBaseline(url, out string) (float64, error) {
	defer os.Remove(out)
	start := time.Now()
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	f, err := os.Create(out)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return 0, err
	}
	return time.Since(start).Seconds(), nil
}

func runQueue(url, out string) (float64, error) {
	defer os.Remove(out)
	e := engine.New(engine.Options{URLs: []string{url}})
	start := time.Now()
	err := e.DownloadQueue(context.Background(), engine.QueueOptions{
		URLs:       []string{url},
		OutputPath: out,
		Workers:    16,
		PieceSize:  2 * 1024 * 1024,
	})
	if err != nil {
		return 0, err
	}
	return time.Since(start).Seconds(), nil
}

func runAdaptive(url, out string) (float64, error) {
	defer os.Remove(out)
	e := engine.New(engine.Options{URLs: []string{url}})
	start := time.Now()
	err := e.DownloadAdaptive(context.Background(), engine.AdaptiveOptions{
		URLs:       []string{url},
		OutputPath: out,
	})
	if err != nil {
		return 0, err
	}
	return time.Since(start).Seconds(), nil
}

func cleanupTmp() {
	os.Remove("ft_baseline.tmp")
	os.Remove("ft_queue.tmp")
	os.Remove("ft_adaptive.tmp")
}

// --- minimal in-process tracker for the swarm test (self-contained, no
// separate tracker.exe needed) ---

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

func (t *miniTracker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/announce", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FileHash string      `json:"file_hash"`
			Addr     string      `json:"addr"`
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

func runSwarmPair(url string, adaptive bool) (c1, c2 float64, err error) {
	tr := &miniTracker{swarm: make(map[string]map[string]*peerInfo)}
	srv := &http.Server{Addr: "localhost:9199", Handler: tr.handler()}
	go srv.ListenAndServe()
	defer srv.Close()
	time.Sleep(300 * time.Millisecond)

	trackerURL := "http://localhost:9199"
	defer os.Remove("ft_swarm1.tmp")
	defer os.Remove("ft_swarm2.tmp")

	run := func(out, listenAddr string) (float64, error) {
		e := engine.New(engine.Options{URLs: []string{url}})
		start := time.Now()
		var rerr error
		if adaptive {
			rerr = e.DownloadAdaptive(context.Background(), engine.AdaptiveOptions{
				URLs:       []string{url},
				OutputPath: out,
				TrackerURL: trackerURL,
				ListenAddr: listenAddr,
			})
		} else {
			rerr = e.DownloadQueue(context.Background(), engine.QueueOptions{
				URLs:       []string{url},
				OutputPath: out,
				Workers:    16,
				PieceSize:  2 * 1024 * 1024,
				TrackerURL: trackerURL,
				ListenAddr: listenAddr,
			})
		}
		if rerr != nil {
			return 0, rerr
		}
		return time.Since(start).Seconds(), nil
	}

	c1, err = run("ft_swarm1.tmp", "localhost:9201")
	if err != nil {
		return 0, 0, err
	}

	time.Sleep(1 * time.Second)

	c2, err = run("ft_swarm2.tmp", "localhost:9202")
	if err != nil {
		return c1, 0, err
	}

	return c1, c2, nil
}
