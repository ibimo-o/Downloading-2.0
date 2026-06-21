package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/ibimo-o/Downloading-2.0/internal/engine"
)

// benchResult holds the aggregated outcome of multiple trials for one config.
type benchResult struct {
	Label      string
	Trials     []float64 // seconds, one per successful trial
	FailedRuns int
}

func (r benchResult) avg() float64 {
	if len(r.Trials) == 0 {
		return 0
	}
	var sum float64
	for _, t := range r.Trials {
		sum += t
	}
	return sum / float64(len(r.Trials))
}

func (r benchResult) min() float64 {
	if len(r.Trials) == 0 {
		return 0
	}
	m := r.Trials[0]
	for _, t := range r.Trials {
		if t < m {
			m = t
		}
	}
	return m
}

func (r benchResult) max() float64 {
	if len(r.Trials) == 0 {
		return 0
	}
	m := r.Trials[0]
	for _, t := range r.Trials {
		if t > m {
			m = t
		}
	}
	return m
}

const trialsPerConfig = 3

func main() {
	url := "http://speedtest.tele2.net/100MB.zip"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}

	fmt.Printf("Benchmarking against: %s\n", url)
	fmt.Printf("Running %d trials per configuration to average out network noise...\n\n", trialsPerConfig)

	var results []benchResult

	baseline := benchResult{Label: "Baseline (single connection)"}
	for i := 0; i < trialsPerConfig; i++ {
		fmt.Printf("Baseline trial %d/%d...\n", i+1, trialsPerConfig)
		secs, err := runBaseline(url)
		if err != nil {
			fmt.Printf("  -> failed: %v\n", err)
			baseline.FailedRuns++
			continue
		}
		fmt.Printf("  -> %.2fs\n", secs)
		baseline.Trials = append(baseline.Trials, secs)
	}
	fmt.Println()
	results = append(results, baseline)

	connCounts := []int{4, 8, 16, 32}
	for _, n := range connCounts {
		r := benchResult{Label: fmt.Sprintf("dl2 queue, %d connections", n)}
		for i := 0; i < trialsPerConfig; i++ {
			fmt.Printf("dl2 (%d conn) trial %d/%d...\n", n, i+1, trialsPerConfig)
			secs, err := runDL2(url, n)
			if err != nil {
				fmt.Printf("  -> failed: %v\n", err)
				r.FailedRuns++
				continue
			}
			fmt.Printf("  -> %.2fs\n", secs)
			r.Trials = append(r.Trials, secs)
		}
		fmt.Println()
		results = append(results, r)
	}

	printReport(results)
}

func runBaseline(url string) (float64, error) {
	start := time.Now()
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	out, err := os.Create("bench_baseline.tmp")
	if err != nil {
		return 0, err
	}
	defer out.Close()
	defer os.Remove("bench_baseline.tmp")

	if _, err := io.Copy(out, resp.Body); err != nil {
		return 0, err
	}
	return time.Since(start).Seconds(), nil
}

func runDL2(url string, connections int) (float64, error) {
	outFile := fmt.Sprintf("bench_dl2_%d.tmp", connections)
	defer os.Remove(outFile)

	e := engine.New(engine.Options{URLs: []string{url}})
	ctx := context.Background()

	start := time.Now()
	err := e.DownloadQueue(ctx, engine.QueueOptions{
		URLs:       []string{url},
		OutputPath: outFile,
		Workers:    connections,
		PieceSize:  2 * 1024 * 1024,
	})
	if err != nil {
		return 0, err
	}
	return time.Since(start).Seconds(), nil
}

func printReport(results []benchResult) {
	valid := results[:0:0]
	for _, r := range results {
		if len(r.Trials) > 0 {
			valid = append(valid, r)
		}
	}
	if len(valid) == 0 {
		fmt.Println("No successful runs to report.")
		return
	}

	baselineAvg := valid[0].avg()

	fmt.Println("=================================================================")
	fmt.Println(" BENCHMARK REPORT  (avg of up to", trialsPerConfig, "trials per config)")
	fmt.Println("=================================================================")
	fmt.Printf("%-30s %8s %8s %8s %9s %6s\n", "Method", "Avg(s)", "Min(s)", "Max(s)", "Speedup", "Fails")
	fmt.Println("-----------------------------------------------------------------")
	for _, r := range valid {
		avg := r.avg()
		speedup := baselineAvg / avg
		fmt.Printf("%-30s %8.2f %8.2f %8.2f %8.2fx %6d\n",
			r.Label, avg, r.min(), r.max(), speedup, r.FailedRuns)
	}
	fmt.Println("=================================================================")

	sorted := make([]benchResult, len(valid))
	copy(sorted, valid)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].avg() < sorted[j].avg() })
	fmt.Printf("\nFastest (by average): %s (%.2fs avg, %.2fx faster than baseline)\n",
		sorted[0].Label, sorted[0].avg(), baselineAvg/sorted[0].avg())

	fmt.Println("\nNote: if Min/Max spread is wide for a config, your network was")
	fmt.Println("unstable during those trials -- the average is still meaningful,")
	fmt.Println("but treat single-run numbers from this benchmark with caution.")
}
