// Command sessile-mcp is the agent's end of the `sessile` MCP server
// (PROJECT_PLAN.md §4.17.3): an ordinary stdio MCP server as far as the agent
// can tell, which relays its stdin and stdout to sessile over the task's
// tunnel.
//
// It lives in <task folder>/.tools/ and finds everything relative to itself:
// the tunnel (.sessile.sock, or .sessile-port for a TCP forward) and this
// start's token (.sessile-token), all in the task folder. It takes no flags
// and no configuration, so the agent's MCP config only has to name it.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sessile-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	taskDir := filepath.Dir(filepath.Dir(exe))
	token, err := os.ReadFile(filepath.Join(taskDir, ".sessile-token"))
	if err != nil {
		return fmt.Errorf("no task token (is the task session running?): %w", err)
	}
	conn, err := dial(taskDir)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "SESSILE-TOKEN %s\n", strings.TrimSpace(string(token))); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(os.Stdout, conn)
		close(done)
	}()
	_, _ = io.Copy(conn, os.Stdin)
	if c, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	}
	<-done
	return nil
}

func dial(taskDir string) (net.Conn, error) {
	sock := filepath.Join(taskDir, ".sessile.sock")
	if _, err := os.Stat(sock); err == nil {
		return net.DialTimeout("unix", sock, 5*time.Second)
	}
	port, err := os.ReadFile(filepath.Join(taskDir, ".sessile-port"))
	if err != nil {
		return nil, fmt.Errorf("no tunnel in %s (is the task session running?)", taskDir)
	}
	p := strings.TrimSpace(string(port))
	// Loopback on the host; inside a Docker Desktop container the host's
	// loopback is reached as host.docker.internal.
	var lastErr error
	for _, host := range []string{"127.0.0.1", "host.docker.internal"} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, p), 3*time.Second)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
