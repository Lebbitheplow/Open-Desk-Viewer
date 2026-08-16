package httpx

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP returns the address to attribute a request to, for rate limiting and
// for the login throttle.
//
// Caddy terminates TLS in front of the API and sets X-Real-IP, so that header
// is preferred. It is only trustworthy because nothing reaches the API except
// through the proxy: if the API is ever exposed directly, a caller can forge
// both headers and the throttle becomes bypassable. r.RemoteAddr is the
// fallback and is not forgeable.
//
// An empty return means "no usable address", which callers treat as "apply no
// per-IP limit" rather than as a wildcard match.
func ClientIP(r *http.Request) string {
	if ip := parseIP(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}

	// The left-most entry is the original client; the rest are proxies.
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		if ip := parseIP(first); ip != "" {
			return ip
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return parseIP(r.RemoteAddr)
	}
	return parseIP(host)
}

// parseIP trims and validates, so a malformed header never reaches a query as
// an INET parameter.
func parseIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if net.ParseIP(value) == nil {
		return ""
	}
	return value
}
