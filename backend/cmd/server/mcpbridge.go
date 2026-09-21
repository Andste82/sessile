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

// The agent's end of the sessile MCP server (PROJECT_PLAN.md §4.12.4, v0.9).
//
// The agent runs on this machine, so this is the whole of it: an ordinary
// stdio MCP server as far as the CLI can tell, relaying its stdin and stdout
// to the socket in the task's own folder. It is a mode of the server binary
// rather than a program of its own — there is nothing to upload, nothing to
// cross-compile, and nothing to keep in step with a released version.
//
//	sessile mcp-bridge <task dir>
func runMCPBridge(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: sessile mcp-bridge <task dir>")
	}
	dir := args[0]
	token, err := os.ReadFile(filepath.Join(dir, ".sessile-token"))
	if err != nil {
		return fmt.Errorf("no task token (is the task running?): %w", err)
	}
	conn, err := dialTask(dir)
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

func dialTask(dir string) (net.Conn, error) {
	sock := filepath.Join(dir, ".sessile.sock")
	if _, err := os.Stat(sock); err == nil {
		return net.DialTimeout("unix", sock, 5*time.Second)
	}
	port, err := os.ReadFile(filepath.Join(dir, ".sessile-port"))
	if err != nil {
		return nil, fmt.Errorf("no tools socket in %s (is the task running?)", dir)
	}
	return net.DialTimeout("tcp", "127.0.0.1:"+strings.TrimSpace(string(port)), 5*time.Second)
}
