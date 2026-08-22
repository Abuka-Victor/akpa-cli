package main

import (
	"akpa/cli/internal/banner"
	"akpa/cli/internal/config"
	tcpclient "akpa/cli/tcp_client"

	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Stamped at build time by GoReleaser. Stays "dev" for local builds.
var version = "dev"

// How long to wait for the relay to send a tunnel id after connecting.
const handshakeTimeout = 10 * time.Second

func main() {
	// ---- 1. config -------------------------------------------------------
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		banner.Errorf("%v", err)
		os.Exit(1)
	}

	if cfg.ShowVer {
		fmt.Println("akpa", version)
		return
	}

	if !cfg.NoBanner {
		banner.Print(version)
	}

	// ---- 2. start the local file server ----------------------------------
	// Listen first, THEN serve. This way we learn the real port before
	// anything tries to use it, and port 0 means "pick a free one".
	banner.Step(os.Stderr, "starting file server")

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.LocalPort))
	if err != nil {
		banner.Fail(os.Stderr)
		banner.Errorf("cannot listen on port %d: %v", cfg.LocalPort, err)
		banner.Hint("Something else is using that port. Try --port 0 to pick a free one.")
		os.Exit(1)
	}
	localAddr := ln.Addr().String()
	banner.Done(os.Stderr, "http://"+localAddr)

	go func() {
		if err := http.Serve(ln, fileHandler(cfg)); err != nil {
			banner.Errorf("local server stopped: %v", err)
		}
	}()

	// ---- 3. connect to the relay -----------------------------------------
	banner.Step(os.Stderr, "connecting to relay %s", cfg.RelayAddr)

	conn, err := tcpclient.ConnectToServer(cfg.RelayAddr)
	if err != nil {
		banner.Fail(os.Stderr)
		banner.Errorf("cannot reach the relay at %s", cfg.RelayAddr)
		banner.Hint("%v", err)
		banner.Hint("")
		banner.Hint("Is the server running? To use a relay on this machine:")
		banner.Hint("  akpa --relay localhost:8080")
		os.Exit(1)
	}
	defer conn.Close()
	banner.Done(os.Stderr, "")

	// ONE reader for the whole life of this connection. Creating a second
	// one later would discard whatever this one has buffered.
	reader := bufio.NewReader(conn)

	// ---- 4. handshake ----------------------------------------------------
	banner.Step(os.Stderr, "waiting for tunnel id")

	// Without a deadline this blocks forever if the relay connects but never
	// sends anything — which looks exactly like a frozen program.
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))

	line, err := reader.ReadString('\n')
	if err != nil {
		banner.Fail(os.Stderr)
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			banner.Errorf("the relay accepted the connection but sent no tunnel id")
			banner.Hint("akpa expects the server to send the id followed by a")
			banner.Hint("newline as soon as a client connects. Check RunTCPServer.")
		} else {
			banner.Errorf("relay closed during handshake: %v", err)
		}
		os.Exit(1)
	}

	// Clear the deadline — it applies to every later read on this connection,
	// and proxied requests can arrive minutes apart.
	_ = conn.SetReadDeadline(time.Time{})

	tunnelID := strings.TrimSpace(line)
	if tunnelID == "" {
		banner.Fail(os.Stderr)
		banner.Errorf("the relay sent an empty tunnel id")
		os.Exit(1)
	}
	banner.Done(os.Stderr, tunnelID)

	link := fmt.Sprintf("https://akpa.victorabuka.com/live/%s", tunnelID)
	fmt.Fprintln(os.Stderr)
	banner.Ready(os.Stderr, cfg.Dir, link)

	// ---- 5. serve tunnel traffic -----------------------------------------
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTunnel(conn, reader, localAddr)
	}()

	// Wait for either Ctrl-C or the tunnel dying.
	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "\n  stopping.")
	case <-done:
		banner.Errorf("connection to relay lost")
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// local file server
// ---------------------------------------------------------------------------

func fileHandler(cfg *config.Config) http.Handler {
	fs := http.FileServer(http.Dir(cfg.Dir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

		// Only force a download when the user asked for it. Forcing it always
		// means HTML never renders in the browser.
		if cfg.ForceDL {
			full := filepath.Join(cfg.Dir, filepath.Clean("/"+r.URL.Path))
			if info, err := os.Stat(full); err == nil && !info.IsDir() {
				name := filepath.Base(r.URL.Path)
				w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
			}
		}

		fs.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// tunnel
// ---------------------------------------------------------------------------

// client is created once, not per request, so connections to the local
// server get reused.
var client = &http.Client{
	Timeout: 60 * time.Second,
	// Don't follow redirects here — pass them to the browser, which is what
	// the browser expects and what keeps its address bar correct.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func handleTunnel(conn net.Conn, reader *bufio.Reader, localAddr string) {
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			return // relay closed, or the stream desynced
		}

		start := time.Now()
		method, path := req.Method, req.URL.Path

		req.URL.Scheme = "http"
		req.URL.Host = localAddr
		req.RequestURI = "" // must be empty when using client.Do

		resp, err := client.Do(req)
		if err != nil {
			banner.Errorf("local server did not answer: %v", err)
			return
		}

		err = resp.Write(conn)
		status := resp.StatusCode
		resp.Body.Close()

		banner.Request(os.Stderr, method, path, status, time.Since(start).Round(time.Millisecond).String())

		if err != nil {
			return
		}
	}
}
