// Example: using the dl2 SDK directly from Go code, without the CLI.
// Run with: go run ./examples/sdk-usage
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ibimo-o/Downloading-2.0/pkg/dl2"
)

func main() {
	url := "http://speedtest.tele2.net/100MB.zip"
	out := "sdk_example_output.zip"

	fmt.Println("Downloading via SDK (with live progress)...")

	progressCh := make(chan dl2.Progress)
	done := make(chan error, 1)

	go func() {
		done <- dl2.DownloadWithProgress(context.Background(), dl2.Options{
			URL:         url,
			Output:      out,
			Connections: 16,
		}, progressCh)
	}()

	for p := range progressCh {
		pct := 0.0
		if p.TotalBytes > 0 {
			pct = float64(p.DownloadedBytes) / float64(p.TotalBytes) * 100
		}
		fmt.Printf("\r%.1f%% (%.2f MB/s)   ", pct, p.SpeedBytesPerS/1e6)
	}

	if err := <-done; err != nil {
		log.Fatalf("\ndownload failed: %v", err)
	}
	fmt.Printf("\nDone -> %s\n", out)
}
