package hostops

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// linuxPlatform lists processes with `ps`, present on every Linux target by
// construction — it's what a shell itself needs.
type linuxPlatform struct{}

// NewLinuxPlatform returns a Platform for a Linux target.
func NewLinuxPlatform() Platform { return linuxPlatform{} }

func (linuxPlatform) ProcessTree(ctx context.Context, t Transport, rootPID *int) ([]Process, error) {
	res, err := t.Exec(ctx, "ps -eo pid,ppid,comm --no-headers")
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("ps exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	flat, err := parsePS(string(res.Stdout))
	if err != nil {
		return nil, fmt.Errorf("parse ps output: %w", err)
	}
	if rootPID == nil {
		return buildProcessForest(flat), nil
	}
	return buildProcessTree(flat, *rootPID), nil
}

// flatProcess is one process before it's been nested under its parent.
type flatProcess struct {
	pid, ppid int
	command   string
}

func parsePS(output string) ([]flatProcess, error) {
	var out []flatProcess
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue // blank or malformed line — skip rather than fail the whole tree
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		out = append(out, flatProcess{pid: pid, ppid: ppid, command: strings.Join(fields[2:], " ")})
	}
	return out, sc.Err()
}

// buildProcessTree assembles flat into rootPID's descendants. A process
// listing is never guaranteed to be in parent-before-child order, so this
// indexes by parent pid first rather than assuming one.
//
// seen guards against a cycle in the source listing (two processes whose
// ppid points at each other, or a self-referential ppid==pid) — the flat
// list is parsed from data produced *on the target*, and Win32_Process's
// ParentProcessId is well documented to go stale after PID reuse, which
// produces exactly this shape. Without the guard, build recurses forever;
// a Go stack overflow is a fatal error, not a panic, so gin.Recovery()
// cannot catch it and the whole server process dies. In a well-formed
// process tree no pid has two parents, so marking a pid seen the first
// time it's placed is equivalent to a per-path visited set — cheaper, and
// correct either way: a cycle just means the second occurrence is dropped
// rather than walked again.
func buildProcessTree(flat []flatProcess, rootPID int) []Process {
	byParent := make(map[int][]flatProcess, len(flat))
	for _, p := range flat {
		byParent[p.ppid] = append(byParent[p.ppid], p)
	}
	seen := map[int]bool{rootPID: true}
	var build func(pid int) []Process
	build = func(pid int) []Process {
		children := byParent[pid]
		out := make([]Process, 0, len(children))
		for _, c := range children {
			if seen[c.pid] {
				continue
			}
			seen[c.pid] = true
			out = append(out, Process{PID: c.pid, PPID: c.ppid, Command: c.command, Children: build(c.pid)})
		}
		return out
	}
	return build(rootPID)
}

// buildProcessForest returns every process with no visible parent in the
// listing — a process whose ppid isn't any pid in flat — as its own root,
// walked the same cycle-safe way as buildProcessTree. Used for "the whole
// target's tree" instead of a hardcoded root pid: Linux's "1" convention
// (init) has no portable equivalent — Windows has no single well-known
// root, and guessing one (0, say) risks a self-referential entry (System
// Idle Process is commonly pid 0, ppid 0) that would need this same guard
// anyway. This works for any target without assuming its shape.
func buildProcessForest(flat []flatProcess) []Process {
	pids := make(map[int]bool, len(flat))
	for _, p := range flat {
		pids[p.pid] = true
	}
	byParent := make(map[int][]flatProcess, len(flat))
	for _, p := range flat {
		byParent[p.ppid] = append(byParent[p.ppid], p)
	}
	seen := make(map[int]bool, len(flat))
	var build func(pid int) []Process
	build = func(pid int) []Process {
		children := byParent[pid]
		out := make([]Process, 0, len(children))
		for _, c := range children {
			if seen[c.pid] {
				continue
			}
			seen[c.pid] = true
			out = append(out, Process{PID: c.pid, PPID: c.ppid, Command: c.command, Children: build(c.pid)})
		}
		return out
	}

	var roots []Process
	for _, p := range flat {
		if pids[p.ppid] || seen[p.pid] {
			continue // has a visible parent, or already placed under one
		}
		seen[p.pid] = true
		roots = append(roots, Process{PID: p.pid, PPID: p.ppid, Command: p.command, Children: build(p.pid)})
	}
	return roots
}
