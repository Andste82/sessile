package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"syscall"

	"github.com/Andste82/sessile/backend/internal/confine"
)

// Confinement has to be applied between fork and exec, and Go gives no hook
// there — so sessile re-execs itself for one step (PROJECT_PLAN.md §4.12.9):
//
//	sessile confine-exec '<rules json>' -- claude --mcp-config …
//
// This mode applies the Landlock ruleset to itself and then execs the real
// command, which inherits the restriction and cannot lift it. The extra
// process lives for microseconds and leaves nothing behind.
func runConfineExec(args []string) error {
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 1 || sep == len(args)-1 {
		return fmt.Errorf("usage: sessile confine-exec '<rules json>' -- <command> [args...]")
	}
	var rules confine.Rules
	if err := json.Unmarshal([]byte(args[0]), &rules); err != nil {
		return fmt.Errorf("read the rules: %w", err)
	}
	// Landlock restricts the calling thread, and exec must happen on that
	// same thread or the new program would start unrestricted. Go moves
	// goroutines between threads freely, so pin this one first.
	runtime.LockOSThread()
	if err := confine.Apply(rules); err != nil {
		return fmt.Errorf("confine this agent: %w", err)
	}
	argv := args[sep+1:]
	// Exec, not spawn: the agent replaces this process, so nothing is left
	// between sessile and the CLI, and the PTY stays the agent's own.
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		return fmt.Errorf("start %s: %w", argv[0], err)
	}
	return nil
}
