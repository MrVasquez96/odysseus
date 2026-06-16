package server

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// contextKey is an unexported type for context keys to avoid collisions.
type contextKey int

const (
	// cspNonceKey is the context key for the per-request CSP nonce.
	cspNonceKey contextKey = iota
)

// CSPNonce retrieves the CSP nonce from the request context.
func CSPNonce(ctx context.Context) string {
	if v, ok := ctx.Value(cspNonceKey).(string); ok {
		return v
	}
	return ""
}

// generateNonce creates a 16-byte hex nonce (matching Python's secrets.token_hex(16)).
func generateNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "fallback-nonce"
	}
	return hex.EncodeToString(b)
}

// --- Request Timeout Middleware ---

// Timeout-exempt path prefixes (from app.py lines 133-143).
var timeoutExemptPrefixes = []string{
	"/api/chat",
	"/api/shell/stream",
	"/api/research",
	"/api/model/download",
	"/api/model/probe",
	"/api/model-endpoints",
	"/api/cookbook/setup",
	"/api/upload",
	"/api/image",
}

// TimeoutMiddleware aborts requests that exceed the hard timeout.
// SSE and other long-running endpoints are exempt.
func TimeoutMiddleware(timeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		for _, prefix := range timeoutExemptPrefixes {
			if strings.HasPrefix(path, prefix) {
				next.ServeHTTP(w, r)
				return
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		done := make(chan struct{})
		go func() {
			next.ServeHTTP(w, r.WithContext(ctx))
			close(done)
		}()

		select {
		case <-done:
			// Request completed normally.
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				http.Error(w, `{"detail":"Request exceeded timeout"}`, http.StatusGatewayTimeout)
			}
		}
	})
}

// --- Security Headers Middleware ---

// SecurityHeadersMiddleware replicates core/middleware.py SecurityHeadersMiddleware.
// It generates a per-request CSP nonce and sets all security headers.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := generateNonce()
		ctx := context.WithValue(r.Context(), cspNonceKey, nonce)
		r = r.WithContext(ctx)

		// Wrap the response writer to intercept headers after the handler runs.
		sw := &securityWriter{ResponseWriter: w, request: r, nonce: nonce}
		next.ServeHTTP(sw, r)
	})
}

type securityWriter struct {
	http.ResponseWriter
	request     *http.Request
	nonce       string
	wroteHeader bool
}

func (sw *securityWriter) WriteHeader(code int) {
	if sw.wroteHeader {
		sw.ResponseWriter.WriteHeader(code)
		return
	}
	sw.wroteHeader = true

	h := sw.ResponseWriter.Header()
	path := sw.request.URL.Path

	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Permissions-Policy", "camera=(), microphone=(self), geolocation=()")

	// HSTS on HTTPS
	isHTTPS := sw.request.TLS != nil ||
		sw.request.Header.Get("X-Forwarded-Proto") == "https"
	if isHTTPS {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}

	// Route-specific CSP variants (matching core/middleware.py)
	isToolRender := strings.HasPrefix(path, "/api/tools/") && strings.HasSuffix(path, "/render")
	isDocPDFPreview := strings.HasPrefix(path, "/api/document/") && strings.HasSuffix(path, "/render-pdf")
	isReport := strings.HasPrefix(path, "/api/research/report/")

	if isReport {
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"font-src 'self'; "+
				"img-src 'self' data: blob: https:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'")
	} else if isToolRender {
		// Tool iframe: skip framing headers, let route's own CSP apply.
	} else if isDocPDFPreview {
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Content-Security-Policy",
			"default-src 'none'; frame-ancestors 'self'")
	} else {
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'nonce-"+sw.nonce+"' https://cdn.jsdelivr.net; "+
				"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; "+
				"font-src 'self' https://cdn.jsdelivr.net; "+
				"img-src 'self' data: blob:; "+
				"media-src 'self' blob:; "+
				"connect-src 'self'; "+
				"frame-src 'self'; "+
				"frame-ancestors 'none'")
	}

	sw.ResponseWriter.WriteHeader(code)
}

func (sw *securityWriter) Write(b []byte) (int, error) {
	if !sw.wroteHeader {
		sw.WriteHeader(http.StatusOK)
	}
	return sw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher for SSE compatibility through the proxy.
func (sw *securityWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// --- Gzip Middleware ---

const gzipMinSize = 1024

// GzipMiddleware compresses responses >= 1024 bytes at level 6.
// text/event-stream is never compressed (SSE must not be buffered).
// Matches Python's GZipMiddleware(minimum_size=1024, compresslevel=6).
func GzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't compress if client doesn't accept gzip.
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		gw := &gzipResponseWriter{
			ResponseWriter: w,
			request:        r,
		}
		defer gw.Close()

		next.ServeHTTP(gw, r)
	})
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, 6)
		return w
	},
}

type gzipResponseWriter struct {
	http.ResponseWriter
	request     *http.Request
	gzWriter    *gzip.Writer
	buf         []byte
	wroteHeader bool
	statusCode  int
	skipGzip    bool
}

func (gw *gzipResponseWriter) WriteHeader(code int) {
	gw.statusCode = code
	gw.wroteHeader = true
	// Don't flush headers yet — wait for first Write to decide on gzip.
}

func (gw *gzipResponseWriter) Write(b []byte) (int, error) {
	if !gw.wroteHeader {
		gw.statusCode = http.StatusOK
		gw.wroteHeader = true
	}

	// Check if we should skip gzip for this response.
	if gw.gzWriter == nil && !gw.skipGzip {
		h := gw.ResponseWriter.Header()
		ct := h.Get("Content-Type")
		// Never compress SSE streams.
		if strings.HasPrefix(ct, "text/event-stream") {
			gw.skipGzip = true
		}
		// Never double-compress (upstream already gzipped).
		if h.Get("Content-Encoding") != "" {
			gw.skipGzip = true
		}
	}

	if gw.skipGzip {
		if len(gw.buf) > 0 {
			// Flush buffered data without gzip.
			gw.ResponseWriter.WriteHeader(gw.statusCode)
			gw.ResponseWriter.Write(gw.buf)
			gw.buf = nil
		} else if gw.gzWriter == nil {
			gw.ResponseWriter.WriteHeader(gw.statusCode)
		}
		return gw.ResponseWriter.Write(b)
	}

	// Buffer until we have enough data to decide.
	gw.buf = append(gw.buf, b...)

	if len(gw.buf) >= gzipMinSize && gw.gzWriter == nil {
		// Enable gzip.
		gw.gzWriter = gzipWriterPool.Get().(*gzip.Writer)
		gw.gzWriter.Reset(gw.ResponseWriter)
		gw.ResponseWriter.Header().Set("Content-Encoding", "gzip")
		gw.ResponseWriter.Header().Del("Content-Length")
		gw.ResponseWriter.WriteHeader(gw.statusCode)
		n, err := gw.gzWriter.Write(gw.buf)
		gw.buf = nil
		return n, err
	}

	if gw.gzWriter != nil {
		return gw.gzWriter.Write(b)
	}

	return len(b), nil
}

func (gw *gzipResponseWriter) Close() {
	if gw.gzWriter != nil {
		gw.gzWriter.Close()
		gzipWriterPool.Put(gw.gzWriter)
	} else if len(gw.buf) > 0 {
		// Small response that didn't hit the threshold — write uncompressed.
		gw.ResponseWriter.WriteHeader(gw.statusCode)
		gw.ResponseWriter.Write(gw.buf)
	}
}

func (gw *gzipResponseWriter) Flush() {
	// While still buffering (undecided on gzip), do NOT flush the
	// underlying writer — that would send headers to the wire before
	// Content-Encoding is set. Only propagate once committed.
	if gw.gzWriter == nil && !gw.skipGzip {
		return
	}
	if gw.gzWriter != nil {
		gw.gzWriter.Flush()
	}
	if f, ok := gw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
