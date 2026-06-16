// Package proxy provides a reverse proxy to the Python backend.
//
// The proxy transparently forwards all API requests from the Go server
// to the Python ML worker, with special handling for SSE (Server-Sent Events)
// streams to ensure they flush immediately without buffering.
package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"

	"odysseus/internal/routes"
)

// New creates a reverse proxy that forwards requests to the Python backend.
//
// SSE streams are flushed immediately (FlushInterval = -1) to prevent
// buffering. The Odysseus frontend uses fetch() + ReadableStream.getReader()
// (NOT EventSource), so the proxy must preserve the exact \n\n framing
// and heartbeat comments.
//
// internalToken is set on every proxied request so Python's request.state
// gets populated even though Python's own AuthMiddleware is disabled.
func New(pythonAddr, internalToken string) http.Handler {
	target, err := url.Parse(pythonAddr)
	if err != nil {
		slog.Error("invalid python backend address", "addr", pythonAddr, "err", err)
		panic("invalid ODYSSEUS_PYTHON_ADDR: " + err.Error())
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Save the default Director so we can extend it.
	defaultDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		defaultDirector(req)

		// The default Director adds X-Forwarded-For, which makes Python's
		// _is_trusted_loopback() return false (it rejects loopback requests
		// that carry proxy headers). Since Go→Python is internal loopback,
		// strip the headers that would break the internal-tool bypass.
		req.Header.Del("X-Forwarded-For")
		req.Header.Del("X-Forwarded-Host")
		req.Header.Del("X-Forwarded-Proto")

		// Go's gzip middleware handles compression for all responses.
		// Strip Accept-Encoding so Python doesn't also compress,
		// which would cause double-gzip (garbled data in browser).
		req.Header.Del("Accept-Encoding")

		// Inject the authenticated username into the proxied request.
		// Python's auth middleware internal-tool bypass reads
		// X-Odysseus-Owner to set request.state.current_user.
		if username, ok := req.Context().Value(routes.CtxKeyCurrentUser).(string); ok && username != "" {
			req.Header.Set("X-Odysseus-Owner", username)
		}

		// Set internal tool token so Python trusts this as an internal
		// request via the internal-tool bypass in its auth middleware.
		if internalToken != "" {
			req.Header.Set("X-Odysseus-Internal-Token", internalToken)
		}
	}

	// FlushInterval = -1 tells the proxy to flush after every write.
	// This is critical for SSE: without it, Go buffers the response and
	// the frontend sees nothing until the stream ends or the buffer fills.
	proxy.FlushInterval = -1

	proxy.ModifyResponse = func(resp *http.Response) error {
		ct := resp.Header.Get("Content-Type")
		if ct == "text/event-stream" || ct == "text/event-stream; charset=utf-8" {
			// Prevent any downstream proxy from buffering SSE.
			resp.Header.Set("X-Accel-Buffering", "no")
			resp.Header.Set("Cache-Control", "no-cache")
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("proxy error", "path", r.URL.Path, "err", err)
		if !headersSent(w) {
			http.Error(w, "Backend unavailable", http.StatusBadGateway)
		}
	}

	return proxy
}

// headersSent checks if response headers have already been written.
// Once headers are sent we can't change the status code.
func headersSent(w http.ResponseWriter) bool {
	// If Content-Type is set, headers were likely already sent.
	return w.Header().Get("Content-Type") != ""
}
