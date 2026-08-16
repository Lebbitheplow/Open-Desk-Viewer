package httpx

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func requestFrom(ip string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/login", nil)
	r.Header.Set("X-Real-IP", ip)
	return r
}

// The burst is what a page load is allowed; the request after it is refused.
func TestRateLimitRefusesOverBurst(t *testing.T) {
	handler := RateLimitMiddleware(NewRateLimiter(60, 3))(okHandler())

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, requestFrom("198.51.100.7"))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d within the burst got %d, want 200", i+1, w.Code)
		}
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, requestFrom("198.51.100.7"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the request past the burst got %d, want 429", w.Code)
	}

	// A Retry-After of 0 would invite an immediate retry that also fails.
	retry := w.Header().Get("Retry-After")
	seconds, err := strconv.Atoi(retry)
	if err != nil || seconds < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", retry)
	}
}

// One noisy address must not throttle everybody else, which is what a single
// global counter would do.
func TestRateLimitIsPerClient(t *testing.T) {
	handler := RateLimitMiddleware(NewRateLimiter(60, 1))(okHandler())

	for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, requestFrom(ip))
		if w.Code != http.StatusOK {
			t.Fatalf("first request from %s got %d, want 200", ip, w.Code)
		}
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, requestFrom("203.0.113.1"))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("second request from the same address got %d, want 429", w.Code)
	}
}

// A body over the cap must be refused rather than read into memory.
func TestMaxBodyMiddlewareRejectsOversizedBody(t *testing.T) {
	var readErr error
	handler := MaxBodyMiddleware(16)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		for {
			_, err := r.Body.Read(buf)
			if err != nil {
				if err.Error() != "EOF" {
					readErr = err
				}
				return
			}
		}
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(strings.Repeat("x", 4096)))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Error("a 4 KiB body read past a 16 byte cap without an error")
	}
}
