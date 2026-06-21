package chunk

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// Chunk represents one byte-range slice of the target file.
type Chunk struct {
	Index      int    // chunk order
	Start      int64  // inclusive byte offset
	End        int64  // inclusive byte offset
	SourceURL  string // which mirror/source this chunk was pulled from
	TempPath   string // where the chunk bytes are stored on disk while downloading
	SHA256     string // expected hash, if known ahead of time (optional)
	Downloaded bool
}

// Size returns the number of bytes in this chunk.
func (c *Chunk) Size() int64 {
	return c.End - c.Start + 1
}

// Plan splits a file of totalSize bytes into n roughly-equal chunks.
// Kept for backward compatibility (Phase 1 fixed-chunk mode).
func Plan(totalSize int64, n int, sourceURLs []string) []*Chunk {
	if n < 1 {
		n = 1
	}
	chunkSize := totalSize / int64(n)
	if chunkSize == 0 {
		chunkSize = totalSize
		n = 1
	}

	chunks := make([]*Chunk, 0, n)
	var start int64 = 0
	for i := 0; i < n; i++ {
		end := start + chunkSize - 1
		if i == n-1 || end > totalSize-1 {
			end = totalSize - 1
		}
		src := sourceURLs[i%len(sourceURLs)] // round-robin across available mirrors
		chunks = append(chunks, &Chunk{
			Index:     i,
			Start:     start,
			End:       end,
			SourceURL: src,
		})
		start = end + 1
		if start >= totalSize {
			break
		}
	}
	return chunks
}

// PlanQueue splits a file into many small, equal-size pieces, independent of
// the number of worker connections. This is the basis of Phase 2's
// work-queue model: instead of N workers each owning one big fixed chunk
// (where one slow chunk stalls the whole job), we create many smaller
// pieces pulled from a shared queue — a slow piece only slows the one
// worker handling it, not the overall job.
func PlanQueue(totalSize int64, pieceSize int64) []*Chunk {
	if pieceSize <= 0 {
		pieceSize = 2 * 1024 * 1024 // default 2MB pieces
	}
	if totalSize <= pieceSize {
		return []*Chunk{{Index: 0, Start: 0, End: totalSize - 1}}
	}

	n := int((totalSize + pieceSize - 1) / pieceSize)
	chunks := make([]*Chunk, 0, n)
	var start int64 = 0
	for i := 0; i < n; i++ {
		end := start + pieceSize - 1
		if end > totalSize-1 {
			end = totalSize - 1
		}
		chunks = append(chunks, &Chunk{Index: i, Start: start, End: end})
		start = end + 1
		if start >= totalSize {
			break
		}
	}
	return chunks
}

// VerifySHA256 checks a downloaded chunk file against an expected hash.
// Returns nil if it matches (or if no hash was expected, i.e. verification skipped).
func VerifySHA256(path string, expected string) error {
	if expected == "" {
		return nil // no hash provided for this chunk, skip
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := fmt.Sprintf("%x", h.Sum(nil))
	if got != expected {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expected, got)
	}
	return nil
}

// VerifyWholeFile hashes a fully reassembled file against an expected SHA256.
func VerifyWholeFile(path string, expected string) error {
	return VerifySHA256(path, expected)
}
