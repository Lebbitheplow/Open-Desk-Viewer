package httpx

import (
	"bufio"
	"net"
	"net/http"
)

// WrapResponseWriter wraps http.ResponseWriter for logging
type WrapResponseWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int
}

// NewWrapResponseWriter creates a new wrapper
func NewWrapResponseWriter(w http.ResponseWriter, protoMajor int) *WrapResponseWriter {
	return &WrapResponseWriter{ResponseWriter: w}
}

// WriteHeader records the status and writes it
func (w *WrapResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write records bytes written and writes data
func (w *WrapResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n
	return n, err
}

// Status returns the written status
func (w *WrapResponseWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// BytesWritten returns the number of bytes written
func (w *WrapResponseWriter) BytesWritten() int {
	return w.bytesWritten
}

// Hijack implements http.Hijacker
func (w *WrapResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(interface {
		Hijack() (net.Conn, *bufio.ReadWriter, error)
	})
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

// Flush implements http.Flusher
func (w *WrapResponseWriter) Flush() {
	f, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	f.Flush()
}

// CloseNotify implements http.CloseNotifier
func (w *WrapResponseWriter) CloseNotify() <-chan bool {
	c, ok := w.ResponseWriter.(interface{ CloseNotify() <-chan bool })
	if !ok {
		return nil
	}
	return c.CloseNotify()
}
