// Package tunnel manages a Cloudflare Quick Tunnel subprocess.
//
// A Quick Tunnel exposes the local Odysseus server to the internet via a random
// *.trycloudflare.com URL with zero configuration — no Cloudflare account required.
// The package manages the cloudflared subprocess, auto-detects the binary, and
// parses the generated public URL.
package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// Manager manages the lifecycle of a cloudflared quick tunnel.
type Manager struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	cmd       *exec.Cmd
	publicURL string
	running   bool
	err       error
	done      chan struct{}
}

// Status returns the current tunnel state.
type Status struct {
	Running   bool   `json:"running"`
	PublicURL string `json:"public_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

// NewManager creates a new tunnel manager.
func NewManager() *Manager {
	return &Manager{}
}

// Start launches the cloudflared quick tunnel pointing to the given local address.
// It blocks until the tunnel URL is available or the context expires.
func (m *Manager) Start(ctx context.Context, localAddr string) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("tunnel already running")
	}
	m.mu.Unlock()

	bin, err := findCloudflared()
	if err != nil {
		return fmt.Errorf("cloudflared not found: %w", err)
	}

	// Wait for local server to be ready
	host, port, _ := net.SplitHostPort(localAddr)
	if host == "" {
		host = "localhost"
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 15*time.Second)
	defer waitCancel()
	if err := waitForPort(waitCtx, host, port); err != nil {
		return fmt.Errorf("local server not ready: %w", err)
	}

	procCtx, procCancel := context.WithCancel(ctx)

	originURL := fmt.Sprintf("http://%s", localAddr)
	cmd := exec.CommandContext(procCtx, bin, "tunnel", "--url", originURL)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		procCancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		procCancel()
		return fmt.Errorf("start cloudflared: %w", err)
	}

	done := make(chan struct{})
	urlCh := make(chan string, 1)

	// Scan stderr for the tunnel URL
	go scanForURL(stderrPipe, urlCh)

	// Wait for process exit in background
	go func() {
		waitErr := cmd.Wait()
		m.mu.Lock()
		m.err = waitErr
		m.running = false
		m.mu.Unlock()
		close(done)
	}()

	// Wait for URL, timeout, or cancellation
	select {
	case url := <-urlCh:
		m.mu.Lock()
		m.publicURL = url
		m.running = true
		m.cancel = procCancel
		m.cmd = cmd
		m.done = done
		m.err = nil
		m.mu.Unlock()
		log.Printf("Cloudflare tunnel active: %s → %s", url, originURL)
		return nil
	case <-done:
		procCancel()
		return fmt.Errorf("cloudflared exited before producing a URL")
	case <-time.After(60 * time.Second):
		procCancel()
		return fmt.Errorf("timed out waiting for tunnel URL")
	case <-ctx.Done():
		procCancel()
		return ctx.Err()
	}
}

// Stop gracefully shuts down the tunnel.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running || m.cancel == nil {
		return nil
	}

	m.cancel()

	// Wait for process to exit
	if m.done != nil {
		select {
		case <-m.done:
		case <-time.After(10 * time.Second):
			if m.cmd != nil && m.cmd.Process != nil {
				m.cmd.Process.Kill()
			}
		}
	}

	m.running = false
	m.publicURL = ""
	log.Println("Cloudflare tunnel stopped")
	return nil
}

// Status returns the current tunnel state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := Status{Running: m.running, PublicURL: m.publicURL}
	if m.err != nil {
		s.Error = m.err.Error()
	}
	return s
}

// ── Helpers ──────────────────────────────────────────────────────────

var urlRe = regexp.MustCompile(`https://[a-zA-Z0-9\-]+\.trycloudflare\.com`)

func scanForURL(r io.Reader, ch chan<- string) {
	scanner := bufio.NewScanner(r)
	sent := false
	for scanner.Scan() {
		line := scanner.Text()
		if !sent {
			if m := urlRe.FindString(line); m != "" {
				ch <- m
				sent = true
			}
		}
	}
}

func findCloudflared() (string, error) {
	// Check $PATH
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}
	// Common install locations
	for _, p := range []string{
		"/usr/local/bin/cloudflared",
		"/usr/bin/cloudflared",
		"/snap/bin/cloudflared",
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("cloudflared binary not found in PATH or common locations; install from https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/installation/")
}

func waitForPort(ctx context.Context, host, port string) error {
	addr := net.JoinHostPort(host, port)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}
