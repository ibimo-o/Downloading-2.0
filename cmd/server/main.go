// Command server exposes the dl2 engine over a small REST API, so anything
// that can make an HTTP request -- a browser extension, a web app, a curl
// script -- can trigger and monitor downloads without needing Go or the CLI.
//
// Endpoints:
//   POST /download           { url, mirrors?, connections?, tracker?, listen? } -> { job_id }
//   GET  /status/{job_id}    -> live progress + state
//   GET  /jobs                -> list all jobs (debugging/dashboard use)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ibimo-o/Downloading-2.0/pkg/dl2"
)

type jobState string

const (
	stateRunning jobState = "running"
	stateDone    jobState = "done"
	stateFailed  jobState = "failed"
)

type job struct {
	ID       string         `json:"id"`
	URL      string         `json:"url"`
	Output   string         `json:"output"`
	State    jobState       `json:"state"`
	Error    string         `json:"error,omitempty"`
	Progress dl2.Progress   `json:"progress"`
	Started  time.Time      `json:"started"`
	Finished time.Time      `json:"finished,omitempty"`
}

type downloadRequest struct {
	URL         string   `json:"url"`
	Mirrors     []string `json:"mirrors"`
	Output      string   `json:"output"`
	Connections int      `json:"connections"`
	Tracker     string   `json:"tracker"`
	Listen      string   `json:"listen"`
}

type server struct {
	mu           sync.RWMutex
	jobs         map[string]*job
	nextID       int
	downDir      string
	allowPrivate bool
}

func newServer(downDir string, allowPrivate bool) *server {
	return &server{jobs: make(map[string]*job), downDir: downDir, allowPrivate: allowPrivate}
}

func (s *server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req downloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	if err := validateDownloadURL(req.URL, s.allowPrivate); err != nil {
		http.Error(w, fmt.Sprintf("rejected: %v", err), http.StatusForbidden)
		return
	}
	for _, m := range req.Mirrors {
		if err := validateDownloadURL(m, s.allowPrivate); err != nil {
			http.Error(w, fmt.Sprintf("mirror rejected: %v", err), http.StatusForbidden)
			return
		}
	}

	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("job-%d", s.nextID)
	out := req.Output
	if out == "" {
		out = filepath.Join(s.downDir, fmt.Sprintf("%s_%s", id, filepath.Base(req.URL)))
	}
	j := &job{ID: id, URL: req.URL, Output: out, State: stateRunning, Started: time.Now()}
	s.jobs[id] = j
	s.mu.Unlock()

	progressCh := make(chan dl2.Progress, 8)

	go func() {
		for p := range progressCh {
			s.mu.Lock()
			j.Progress = p
			s.mu.Unlock()
		}
	}()

	go func() {
		conns := req.Connections
		if conns <= 0 {
			conns = 16
		}
		err := dl2.DownloadWithProgress(context.Background(), dl2.Options{
			URL:         req.URL,
			Mirrors:     req.Mirrors,
			Output:      out,
			Connections: conns,
			TrackerURL:  req.Tracker,
			ListenAddr:  req.Listen,
		}, progressCh)

		s.mu.Lock()
		j.Finished = time.Now()
		if err != nil {
			j.State = stateFailed
			j.Error = err.Error()
		} else {
			j.State = stateDone
		}
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"job_id": id})
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/status/")
	s.mu.RLock()
	j, ok := s.jobs[id]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(j)
}

func (s *server) handleJobs(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, j)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// withCORS allows the browser extension (and any local web page) to call
// this API directly from the browser without being blocked by CORS.
func withCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		h(w, r)
	}
}

// withAuth enforces an optional shared-secret token. If token is empty,
// auth is disabled entirely (intended for localhost/trusted-LAN use only --
// see SECURITY.md). When set, requests must include header X-DL2-Token
// matching it.
func withAuth(token string, h http.HandlerFunc) http.HandlerFunc {
	if token == "" {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-DL2-Token") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func main() {
	port := flag.String("port", "8787", "port to listen on")
	downDir := flag.String("dir", ".", "directory to save downloads in by default")
	token := flag.String("token", "", "optional shared-secret token. If set, callers must include header X-DL2-Token. Leave empty for localhost-only/trusted use without auth.")
	allowPrivate := flag.Bool("allow-private", false, "allow download URLs that resolve to private/internal/link-local IPs (e.g. for local test servers). NEVER enable this on a publicly reachable deployment -- it disables SSRF protection.")
	rateLimit := flag.Float64("rate-limit", 5, "max requests per second per caller IP for /download (token bucket, burst allowed)")
	rateBurst := flag.Float64("rate-burst", 10, "burst size for the per-IP rate limiter")
	flag.Parse()

	s := newServer(*downDir, *allowPrivate)
	rl := newRateLimiter(*rateLimit, *rateBurst)

	http.HandleFunc("/download", withCORS(withRateLimit(rl, withAuth(*token, s.handleDownload))))
	http.HandleFunc("/status/", withCORS(withAuth(*token, s.handleStatus)))
	http.HandleFunc("/jobs", withCORS(withAuth(*token, s.handleJobs)))

	addr := "localhost:" + *port
	log.Printf("dl2 API server listening on http://%s", addr)
	log.Printf("  POST http://%s/download   { \"url\": \"...\" }", addr)
	log.Printf("  GET  http://%s/status/{job_id}", addr)
	if *allowPrivate {
		log.Printf("  WARNING: -allow-private is set, SSRF protection is DISABLED. Local/dev use only.")
	}
	log.Fatal(http.ListenAndServe(addr, nil))
}
