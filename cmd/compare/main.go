// Command compare runs a fair, averaged head-to-head benchmark of dl2
// against aria2c (the closest real competitor) and a plain single-connection
// baseline, all against the same file. Requires aria2c to be installed and
// on PATH -- if it isn't found, the aria2 rows are skipped and the report
// says so explicitly rather than faking a result.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/ibimo-o/Downloading-2.0/pkg/dl2"
)

const trials = 3

type result struct {
	label string
	times []float64
}

func (r result) avg() float64 {
	if len(r.times) == 0 {
		return 0
	}
	var s float64
	for _, t := range r.times {
		s += t
	}
	return s / float64(len(r.times))
}

func main() {
	url := "http://speedtest.tele2.net/100MB.zip"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}

	aria2Available := checkAria2()
	if !aria2Available {
		fmt.Println("NOTE: aria2c not found on PATH. Install it to include it in this comparison:")
		fmt.Println("  Windows: winget install aria2.aria2   (or choco install aria2)")
		fmt.Println("  Then re-run this benchmark.")
		fmt.Println()
	}

	fmt.Printf("Comparing against: %s  (%d trials each)\n\n", url, trials)

	var results []result

	// Baseline: plain single-connection.
	base := result{label: "Baseline (single connection)"}
	for i := 0; i < trials; i++ {
		fmt.Printf("baseline trial %d/%d...\n", i+1, trials)
		t, err := runBaseline(url, fmt.Sprintf("cmp_baseline_%d.tmp", i))
		if err != nil {
			fmt.Printf("  failed: %v\n", err)
			continue
		}
		fmt.Printf("  -> %.2fs\n", t)
		base.times = append(base.times, t)
	}
	results = append(results, base)
	fmt.Println()

	// dl2 (queue mode, 16 connections).
	d := result{label: "dl2 (queue, 16 connections)"}
	for i := 0; i < trials; i++ {
		fmt.Printf("dl2 trial %d/%d...\n", i+1, trials)
		t, err := runDL2(url, fmt.Sprintf("cmp_dl2_%d.tmp", i))
		if err != nil {
			fmt.Printf("  failed: %v\n", err)
			continue
		}
		fmt.Printf("  -> %.2fs\n", t)
		d.times = append(d.times, t)
	}
	results = append(results, d)
	fmt.Println()

	// aria2 (16 connections, comparable settings), only if installed.
	if aria2Available {
		a := result{label: "aria2c (-x16 -s16)"}
		for i := 0; i < trials; i++ {
			fmt.Printf("aria2 trial %d/%d...\n", i+1, trials)
			t, err := runAria2(url, fmt.Sprintf("cmp_aria2_%d.tmp", i))
			if err != nil {
				fmt.Printf("  failed: %v\n", err)
				continue
			}
			fmt.Printf("  -> %.2fs\n", t)
			a.times = append(a.times, t)
		}
		results = append(results, a)
		fmt.Println()
	}

	printReport(results)
	cleanup()
}

func checkAria2() bool {
	_, err := exec.LookPath("aria2c")
	return err == nil
}

func runBaseline(url, out string) (float64, error) {
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
	buf := make([]byte, 64*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			f.Write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	return time.Since(start).Seconds(), nil
}

func runDL2(url, out string) (float64, error) {
	start := time.Now()
	err := dl2.Download(context.Background(), dl2.Options{
		URL:         url,
		Output:      out,
		Connections: 16,
	})
	if err != nil {
		return 0, err
	}
	return time.Since(start).Seconds(), nil
}

func runAria2(url, out string) (float64, error) {
	start := time.Now()
	cmd := exec.Command("aria2c",
		"-x16", "-s16", // 16 connections, 16 splits -- comparable to dl2's 16 workers
		"-o", out,
		"--allow-overwrite=true",
		"--quiet=true",
		url,
	)
	if err := cmd.Run(); err != nil {
		return 0, err
	}
	return time.Since(start).Seconds(), nil
}

func printReport(results []result) {
	var baseline float64
	if len(results) > 0 {
		baseline = results[0].avg()
	}

	fmt.Println("=========================================================")
	fmt.Println(" HEAD-TO-HEAD COMPARISON")
	fmt.Println("=========================================================")
	fmt.Printf("%-32s %10s %10s\n", "Method", "Avg(s)", "Speedup")
	fmt.Println("---------------------------------------------------------")
	for _, r := range results {
		avg := r.avg()
		if avg == 0 {
			fmt.Printf("%-32s %10s %10s\n", r.label, "n/a", "n/a")
			continue
		}
		fmt.Printf("%-32s %10.2f %9.2fx\n", r.label, avg, baseline/avg)
	}
	fmt.Println("=========================================================")
	fmt.Println()
	fmt.Println("Read this as: dl2 vs baseline shows our real speedup.")
	fmt.Println("dl2 vs aria2 shows whether we beat the closest mature")
	fmt.Println("competitor on raw multi-connection speed alone (without")
	fmt.Println("swarm mode, which aria2 has no equivalent for outside")
	fmt.Println("actual .torrent files).")
}

func cleanup() {
	for i := 0; i < trials; i++ {
		os.Remove(fmt.Sprintf("cmp_baseline_%d.tmp", i))
		os.Remove(fmt.Sprintf("cmp_dl2_%d.tmp", i))
		os.Remove(fmt.Sprintf("cmp_aria2_%d.tmp", i))
	}
}
