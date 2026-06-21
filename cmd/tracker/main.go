// Command tracker is the Phase 4 swarm coordinator. Peers "announce"
// themselves (file hash + address + which piece indices they currently
// have), and ask "who has piece N of file X" before falling back to the
// origin server. This is intentionally simple (in-memory, no persistence) --
// it's a coordination point, not a content host. No file bytes ever pass
// through the tracker.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync"
	"time"
)

type peerInfo struct {
	Addr     string      `json:"addr"`       // host:port where this peer's piece-server listens
	Pieces   []pieceHash `json:"pieces"`      // pieces this peer currently has, with hashes
	LastSeen time.Time   `json:"-"`
}

type pieceHash struct {
	Index int    `json:"index"`
	Hash  string `json:"hash"`
}

type announceRequest struct {
	FileHash string      `json:"file_hash"`
	Addr     string      `json:"addr"`
	Pieces   []pieceHash `json:"pieces"`
}

type peersResponse struct {
	Peers []peerInfo `json:"peers"`
}

type tracker struct {
	mu    sync.RWMutex
	swarm map[string]map[string]*peerInfo // fileHash -> peerAddr -> info
}

func newTracker() *tracker {
	return &tracker{swarm: make(map[string]map[string]*peerInfo)}
}

func (t *tracker) announce(req announceRequest) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.swarm[req.FileHash] == nil {
		t.swarm[req.FileHash] = make(map[string]*peerInfo)
	}
	t.swarm[req.FileHash][req.Addr] = &peerInfo{
		Addr:     req.Addr,
		Pieces:   req.Pieces,
		LastSeen: time.Now(),
	}
}

func (t *tracker) peersFor(fileHash, excludeAddr string) []peerInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []peerInfo
	cutoff := time.Now().Add(-2 * time.Minute) // drop stale peers
	for addr, p := range t.swarm[fileHash] {
		if addr == excludeAddr {
			continue
		}
		if p.LastSeen.Before(cutoff) {
			continue
		}
		out = append(out, *p)
	}
	return out
}

func main() {
	port := flag.String("port", "9090", "port to listen on")
	token := flag.String("token", "", "optional shared-secret token. If set, all requests must include header X-DL2-Token matching this value. Leave empty for localhost-only/trusted-LAN use without auth.")
	flag.Parse()

	t := newTracker()

	requireToken := func(h http.HandlerFunc) http.HandlerFunc {
		if *token == "" {
			return h // auth disabled -- localhost/trusted-LAN mode
		}
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-DL2-Token") != *token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}

	http.HandleFunc("/announce", requireToken(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req announceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		t.announce(req)
		w.WriteHeader(http.StatusOK)
	}))

	http.HandleFunc("/peers", requireToken(func(w http.ResponseWriter, r *http.Request) {
		fileHash := r.URL.Query().Get("file_hash")
		exclude := r.URL.Query().Get("exclude")
		if fileHash == "" {
			http.Error(w, "file_hash required", http.StatusBadRequest)
			return
		}
		resp := peersResponse{Peers: t.peersFor(fileHash, exclude)}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))

	addr := ":" + *port
	log.Printf("dl2 swarm tracker listening on %s", addr)
	if *token != "" {
		log.Printf("auth enabled: clients must send header X-DL2-Token")
	} else {
		log.Printf("auth disabled (no -token set) -- localhost/trusted-LAN use only")
	}
	log.Fatal(http.ListenAndServe(addr, nil))
}
