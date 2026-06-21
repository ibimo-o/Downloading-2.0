package main

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a minimal per-IP token bucket: each IP gets `burst`
// requests immediately, then refills at `rate` requests per second. Good
// enough to stop accidental hammering or naive abuse on a small local/LAN
// deployment -- not a substitute for a real API gateway under serious
// public load.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64 // max tokens
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

func newRateLimiter(rate, burst float64) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string]*bucket),
		rate:    rate,
		burst:   burst,
	}
	go rl.cleanupLoop()
	return rl
}

// cleanupLoop periodically forgets IPs that haven't been seen in a while,
// so the bucket map doesn't grow unbounded over a long-running server.
func (rl *rateLimiter) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-30 * time.Minute)
		for ip, b := range rl.buckets {
			if b.lastSeen.Before(cutoff) {
				delete(rl.buckets, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[ip]
	now := time.Now()
	if !ok {
		rl.buckets[ip] = &bucket{tokens: rl.burst - 1, lastSeen: now}
		return true
	}

	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// withRateLimit wraps a handler, rejecting requests over the limit with
// 429 Too Many Requests.
func withRateLimit(rl *rateLimiter, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !rl.allow(ip) {
			http.Error(w, "rate limit exceeded, slow down", http.StatusTooManyRequests)
			return
		}
		h(w, r)
	}
}
