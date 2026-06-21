// Package swarm implements the Phase 4 peer-assisted download layer.
// Each downloading instance can simultaneously serve pieces it already has
// to other peers downloading the same file (matched by file hash), and
// pull pieces from peers instead of the origin server when a peer has them
// available -- usually faster, and it offloads the origin server.
package swarm

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PieceHash pairs a piece index with the SHA-256 hash of its content, so
// peers receiving the piece can verify it wasn't corrupted or tampered
// with before accepting it.
type PieceHash struct {
	Index int    `json:"index"`
	Hash  string `json:"hash"`
}

type PeerInfo struct {
	Addr   string      `json:"addr"`
	Pieces []PieceHash `json:"pieces"`
}

// Store holds pieces this instance has downloaded and is willing to share
// with other peers, keyed by piece index. Thread-safe. Each stored piece
// also carries its SHA-256 hash so receiving peers can verify integrity.
// Store holds pieces this instance has downloaded and is willing to share
// with other peers. Piece bytes are written to disk (not kept in memory),
// so the swarm layer doesn't OOM on large files -- only piece hashes (a
// few dozen bytes each) live in memory, regardless of file size.
type Store struct {
	mu     sync.RWMutex
	hashes map[int]string
	dir    string // directory pieces are cached in on disk
}

// NewStore creates a disk-backed piece store. Pieces are written under
// dir/piece-<index> and cleaned up via Close() when the download finishes.
// If dir is empty, a temp directory is created automatically.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		d, err := os.MkdirTemp("", "dl2-swarm-store-*")
		if err != nil {
			return nil, err
		}
		dir = d
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{hashes: make(map[int]string), dir: dir}, nil
}

// Close removes the on-disk piece cache. Call when the download finishes
// (success or failure) to avoid leaking disk space across runs.
func (s *Store) Close() error {
	return os.RemoveAll(s.dir)
}

func (s *Store) piecePath(index int) string {
	return filepath.Join(s.dir, fmt.Sprintf("piece-%d", index))
}

// Put writes piece data to disk and records its hash. data is not
// retained in memory beyond this call.
func (s *Store) Put(index int, data []byte) error {
	if err := os.WriteFile(s.piecePath(index), data, 0o644); err != nil {
		return err
	}
	s.mu.Lock()
	s.hashes[index] = fmt.Sprintf("%x", sha256.Sum256(data))
	s.mu.Unlock()
	return nil
}

func (s *Store) Hash(index int) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hashes[index]
	return h, ok
}

// Get reads a piece's bytes back from disk. Only called when actually
// serving a piece to a peer, so memory use stays proportional to one piece
// at a time, not the whole file.
func (s *Store) Get(index int) ([]byte, bool) {
	s.mu.RLock()
	_, ok := s.hashes[index]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(s.piecePath(index))
	if err != nil {
		return nil, false
	}
	return data, true
}

func (s *Store) PieceHashes() []PieceHash {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PieceHash, 0, len(s.hashes))
	for i, h := range s.hashes {
		out = append(out, PieceHash{Index: i, Hash: h})
	}
	return out
}

// ServeHTTP exposes stored pieces at GET /piece?index=N so other peers can
// fetch them directly. Streams straight from disk rather than holding the
// piece in memory.
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	idxStr := r.URL.Query().Get("index")
	var idx int
	if _, err := fmt.Sscanf(idxStr, "%d", &idx); err != nil {
		http.Error(w, "bad index", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	_, ok := s.hashes[idx]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(s.piecePath(idx))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

// Client talks to the tracker and to other peers.
type Client struct {
	TrackerURL string
	FileHash   string
	SelfAddr   string // this instance's own host:port, so others can reach it
	AuthToken  string // optional shared-secret sent as X-DL2-Token to the tracker
	Store      *Store
	httpClient *http.Client
}

func NewClient(trackerURL, fileHash, selfAddr, authToken string, store *Store) *Client {
	return &Client{
		TrackerURL: trackerURL,
		FileHash:   fileHash,
		SelfAddr:   selfAddr,
		AuthToken:  authToken,
		Store:      store,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Announce tells the tracker which pieces this instance currently has.
// Call this periodically (e.g. every few seconds) while downloading.
func (c *Client) Announce() error {
	if c.TrackerURL == "" {
		return nil // swarm mode disabled
	}
	body, _ := json.Marshal(map[string]interface{}{
		"file_hash": c.FileHash,
		"addr":      c.SelfAddr,
		"pieces":    c.Store.PieceHashes(),
	})
	req, err := http.NewRequest(http.MethodPost, c.TrackerURL+"/announce", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.AuthToken != "" {
		req.Header.Set("X-DL2-Token", c.AuthToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// AnnouncePiece triggers an immediate (non-blocking) announce right after a
// new piece becomes available, instead of waiting for the next periodic
// tick. Best-effort -- errors are ignored since the periodic ticker is the
// reliability backstop.
func (c *Client) AnnouncePiece() {
	go c.Announce()
}

// Peers asks the tracker who else has pieces of this file right now.
func (c *Client) Peers() ([]PeerInfo, error) {
	if c.TrackerURL == "" {
		return nil, nil
	}
	url := fmt.Sprintf("%s/peers?file_hash=%s&exclude=%s", c.TrackerURL, c.FileHash, c.SelfAddr)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.AuthToken != "" {
		req.Header.Set("X-DL2-Token", c.AuthToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Peers []PeerInfo `json:"peers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Peers, nil
}

// FetchFromPeer pulls one piece directly from another peer's piece-server
// and verifies it against expectedHash before returning it. If the
// received bytes don't match, this returns an error rather than silently
// accepting potentially corrupted/tampered data -- the caller should treat
// this exactly like a failed fetch and fall back to the origin server.
func (c *Client) FetchFromPeer(peerAddr string, index int, expectedHash string) ([]byte, error) {
	url := fmt.Sprintf("http://%s/piece?index=%d", peerAddr, index)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer returned %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if expectedHash != "" {
		got := fmt.Sprintf("%x", sha256.Sum256(data))
		if got != expectedHash {
			return nil, fmt.Errorf("piece %d from peer %s failed integrity check: expected %s, got %s", index, peerAddr, expectedHash, got)
		}
	}

	return data, nil
}

// FindPeerWithPiece checks the known peer list for one that claims to have
// the given piece index. Returns the peer address and the hash that peer
// announced for it (used to verify the fetched bytes), or "" if no peer
// has it.
func FindPeerWithPiece(peers []PeerInfo, index int) (addr string, expectedHash string) {
	for _, p := range peers {
		for _, ph := range p.Pieces {
			if ph.Index == index {
				return p.Addr, ph.Hash
			}
		}
	}
	return "", ""
}

// StartPeerServer starts the local HTTP server other peers will pull
// pieces from. Runs in the background; call once per download.
func StartPeerServer(listenAddr string, store *Store) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/piece", store.ServeHTTP)
	srv := &http.Server{Addr: listenAddr, Handler: mux}
	go srv.ListenAndServe()
	return nil
}
