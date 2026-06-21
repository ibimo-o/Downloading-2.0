package main

import (
	"fmt"
	"net"
	"net/url"
)

// validateDownloadURL rejects URLs that resolve to private, loopback,
// link-local, or cloud-metadata IP addresses, to close the SSRF hole where
// a caller could ask the server to fetch internal-network or
// cloud-metadata endpoints (e.g. 169.254.169.254) via the download API.
//
// allowPrivate bypasses this check entirely -- intended for local
// development/testing only (e.g. downloading from a LAN tracker or a
// local test server). Never enable it on a publicly reachable deployment.
func validateDownloadURL(rawURL string, allowPrivate bool) error {
	if allowPrivate {
		return nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http/https URLs are allowed, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("URL has no host")
	}

	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return fmt.Errorf("could not resolve host %q: %w", u.Hostname(), err)
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("URL resolves to a private/internal/link-local address (%s) -- not allowed for security reasons; use -allow-private only for trusted local/dev use", ip)
		}
	}
	return nil
}

// isDisallowedIP reports whether ip is loopback, private, link-local
// (includes the 169.254.169.254 cloud-metadata address), or otherwise
// not a normal public destination.
func isDisallowedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	// Defense in depth: explicitly block the well-known cloud metadata
	// address even if some platform's IsPrivate()/IsLinkLocalUnicast()
	// classification ever changes.
	if ip.String() == "169.254.169.254" {
		return true
	}
	return false
}
