package chunk

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanBasic(t *testing.T) {
	chunks := Plan(1000, 4, []string{"http://example.com/file"})
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(chunks))
	}

	var total int64
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d has wrong index %d", i, c.Index)
		}
		if c.Start > c.End {
			t.Errorf("chunk %d has start > end (%d > %d)", i, c.Start, c.End)
		}
		total += c.Size()
	}
	if total != 1000 {
		t.Errorf("chunks don't cover full file: got %d bytes, want 1000", total)
	}

	// Chunks must be contiguous with no gaps or overlaps.
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Start != chunks[i-1].End+1 {
			t.Errorf("gap or overlap between chunk %d and %d", i-1, i)
		}
	}
}

func TestPlanSingleConnection(t *testing.T) {
	chunks := Plan(500, 1, []string{"http://example.com/file"})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Start != 0 || chunks[0].End != 499 {
		t.Errorf("expected full-file range [0,499], got [%d,%d]", chunks[0].Start, chunks[0].End)
	}
}

func TestPlanMoreConnectionsThanBytes(t *testing.T) {
	// Asking for more chunks than there are bytes shouldn't crash or
	// produce empty/invalid chunks.
	chunks := Plan(3, 16, []string{"http://example.com/file"})
	var total int64
	for _, c := range chunks {
		if c.Start > c.End {
			t.Errorf("invalid chunk range [%d,%d]", c.Start, c.End)
		}
		total += c.Size()
	}
	if total != 3 {
		t.Errorf("expected total 3 bytes covered, got %d", total)
	}
}

func TestPlanRoundRobinSources(t *testing.T) {
	urls := []string{"http://a.com/f", "http://b.com/f"}
	chunks := Plan(1000, 4, urls)
	for i, c := range chunks {
		want := urls[i%2]
		if c.SourceURL != want {
			t.Errorf("chunk %d: expected source %s, got %s", i, want, c.SourceURL)
		}
	}
}

func TestPlanQueueCoverage(t *testing.T) {
	pieces := PlanQueue(10_000_000, 2_000_000) // 10MB file, 2MB pieces
	if len(pieces) != 5 {
		t.Fatalf("expected 5 pieces, got %d", len(pieces))
	}
	var total int64
	for i, p := range pieces {
		if p.Index != i {
			t.Errorf("piece %d has wrong index %d", i, p.Index)
		}
		total += p.Size()
	}
	if total != 10_000_000 {
		t.Errorf("pieces don't cover full file: got %d, want 10000000", total)
	}
}

func TestPlanQueueSmallerThanPiece(t *testing.T) {
	pieces := PlanQueue(500, 2_000_000)
	if len(pieces) != 1 {
		t.Fatalf("expected 1 piece for a file smaller than piece size, got %d", len(pieces))
	}
	if pieces[0].Size() != 500 {
		t.Errorf("expected piece size 500, got %d", pieces[0].Size())
	}
}

func TestVerifySHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	data := []byte("the quick brown fox jumps over the lazy dog")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	correctHash := fmt.Sprintf("%x", sha256.Sum256(data))

	if err := VerifySHA256(path, correctHash); err != nil {
		t.Errorf("expected hash to match, got error: %v", err)
	}

	if err := VerifySHA256(path, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Error("expected hash mismatch error for wrong hash, got nil")
	}

	// Empty expected hash means "skip verification" -- should never error.
	if err := VerifySHA256(path, ""); err != nil {
		t.Errorf("expected no error when no hash provided, got: %v", err)
	}
}

func TestVerifySHA256MissingFile(t *testing.T) {
	if err := VerifySHA256("/nonexistent/path/file.bin", "deadbeef"); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}
