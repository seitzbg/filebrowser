package fbhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Limit password checks per client; forwarded headers require a trusted peer.
// The bounded table prevents random addresses from growing memory indefinitely.
type attemptWindow struct {
	start time.Time
	count int
}
type attemptLimiter struct {
	mu      sync.Mutex
	entries map[string]attemptWindow
}

var passwordAttempts = attemptLimiter{entries: make(map[string]attemptWindow)}

var passwordWorkers = make(chan struct{}, 4)

func acquirePasswordWorker() (func(), bool) {
	select {
	case passwordWorkers <- struct{}{}:
		return func() { <-passwordWorkers }, true
	default:
		return nil, false
	}
}

func (l *attemptLimiter) allow(r *http.Request) bool {
	host, ok := r.Context().Value(clientIdentityKey{}).(string)
	if !ok {
		host = clientIdentity(r, nil)
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[host]
	if now.Sub(entry.start) >= time.Minute {
		if len(l.entries) >= 4096 {
			for key, value := range l.entries {
				if now.Sub(value.start) >= time.Minute {
					delete(l.entries, key)
				}
			}
			if len(l.entries) >= 4096 {
				return false
			}
		}
		entry = attemptWindow{start: now}
	}
	if entry.count >= 20 {
		return false
	}
	entry.count++
	l.entries[host] = entry
	return true
}

type deadlineBody struct {
	io.ReadCloser
	controller *http.ResponseController
	deadline   time.Time
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	deadline := time.Now().Add(30 * time.Second)
	if !b.deadline.IsZero() && b.deadline.Before(deadline) {
		deadline = b.deadline
	}
	_ = b.controller.SetReadDeadline(deadline)
	return b.ReadCloser.Read(p)
}

type clientIdentityKey struct{}

func clientIdentity(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	isTrusted := func(addr netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	}
	if !isTrusted(peer) {
		return peer.String()
	}
	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if forwarded == "" || len(forwarded) > 4096 {
		return peer.String()
	}
	hops := strings.Split(forwarded, ",")
	if len(hops) > 32 {
		return peer.String()
	}
	// Walk toward the client until the first untrusted hop. Values further
	// left may have been supplied by that client and cannot identify it.
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil || addr.Zone() != "" {
			return peer.String()
		}
		addr = addr.Unmap()
		if !isTrusted(addr) || i == 0 {
			return addr.String()
		}
	}
	return peer.String()
}

func secureHandler(next http.Handler, trustedProxies ...netip.Prefix) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), clientIdentityKey{}, clientIdentity(r, trustedProxies)))
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'self'; base-uri 'self'; object-src 'none'")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.Body != nil {
			controller := http.NewResponseController(w)
			body := &deadlineBody{ReadCloser: r.Body, controller: controller}
			r.Body = body
			deadline := time.Now().Add(30 * time.Second)
			if !strings.HasPrefix(r.URL.Path, "/api/resources") && !strings.HasPrefix(r.URL.Path, "/api/tus") {
				deadline = time.Now().Add(15 * time.Second)
				body.deadline = deadline
				r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			}
			// Keep this deadline through net/http's automatic body drain. The
			// server sets a fresh idle deadline before reusing the connection.
			_ = controller.SetReadDeadline(deadline)
		}
		next.ServeHTTP(w, r)
	})
}
