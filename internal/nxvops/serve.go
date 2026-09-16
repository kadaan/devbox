// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nxvops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"go.jetify.com/devbox/internal/redact"
)

// serverStartTimeout bounds the wait for nxv to answer its health endpoint. The
// index is a multi-gigabyte SQLite database, so a cold start is not instant.
const serverStartTimeout = 2 * time.Minute

// Server is an nxv HTTP server running as a child process.
type Server struct {
	// URL is the base URL the server is listening on.
	URL string

	cmd *exec.Cmd

	// exited is closed once the process is reaped, and waitErr holds its
	// status. A channel carrying the status would only deliver it to the first
	// receiver, and both waitUntilHealthy and Stop need to observe it.
	exited  chan struct{}
	waitErr error
}

// StartServer runs `nxv serve` on a free loopback port and returns once it
// answers its health endpoint. dbPath may be empty to let nxv use its own
// default index location.
//
// evalSystems lets the index evaluate a package for a system it has not
// observed yet, which is what makes a resolve report the version this machine
// will actually get rather than the one the channel snapshot recorded. It
// needs a writable index and costs one nixpkgs evaluation per commit.
//
// The caller must call [Server.Stop] to shut it down; a child process outlives
// its parent otherwise.
func StartServer(ctx context.Context, binary, dbPath string, evalSystems bool) (*Server, error) {
	port, err := freeLoopbackPort()
	if err != nil {
		return nil, err
	}

	args := []string{}
	if dbPath != "" {
		args = append(args, "--db-path", dbPath)
	}
	args = append(args, "serve", "--host", "127.0.0.1", "--port", fmt.Sprint(port))
	if evalSystems {
		args = append(args, "--eval-systems")
	}

	cmd := exec.Command(binary, args...)
	cmd.Stdout = os.Stderr // nxv's logs are diagnostics, not this command's output
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, redact.Errorf("start nxv serve: %w", redact.Safe(err))
	}

	server := &Server{
		URL:    fmt.Sprintf("http://127.0.0.1:%d", port),
		cmd:    cmd,
		exited: make(chan struct{}),
	}
	go func() {
		server.waitErr = cmd.Wait()
		close(server.exited)
	}()

	if err := server.waitUntilHealthy(ctx); err != nil {
		_ = server.Stop()
		return nil, err
	}
	return server, nil
}

// Stop terminates the server and waits for it to exit.
func (s *Server) Stop() error {
	if s.cmd.Process == nil {
		return nil
	}
	// SIGTERM rather than Kill so nxv can close the database cleanly.
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Already gone; collect its status below.
		if !errors.Is(err, os.ErrProcessDone) {
			return redact.Errorf("signal nxv serve: %w", redact.Safe(err))
		}
	}

	select {
	case <-s.exited:
		return nil
	case <-time.After(10 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exited
		return nil
	}
}

func (s *Server) waitUntilHealthy(ctx context.Context) error {
	deadline := time.Now().Add(serverStartTimeout)
	client := &http.Client{Timeout: 5 * time.Second}

	for {
		// A server that has already exited will never become healthy, so fail
		// on that rather than waiting out the whole timeout.
		select {
		case <-s.exited:
			// Not %w of the wait error: an *exec.ExitError in the chain makes
			// devbox report this as an internal fault (see runNxv).
			return redact.Errorf("nxv serve exited before becoming healthy: %s",
				redact.Safe(s.waitErr))
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			s.URL+"/api/v1/health", nil)
		if err != nil {
			return redact.Errorf("check nxv health: %w", redact.Safe(err))
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return redact.Errorf("nxv serve did not become healthy within %s",
				redact.Safe(serverStartTimeout))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// freeLoopbackPort asks the kernel for an unused port. There is a window between
// closing this listener and nxv binding the port; nothing else on a developer
// machine is likely to take it in between.
func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, redact.Errorf("find a free port: %w", redact.Safe(err))
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
