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

func (linuxPlatform) ProcessTree(ctx context.Context, t Transport, rootPID int) ([]Process, error) {
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
	return buildProcessTree(flat, rootPID), nil
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
func buildProcessTree(flat []flatProcess, rootPID int) []Process {
	byParent := make(map[int][]flatProcess, len(flat))
	for _, p := range flat {
		byParent[p.ppid] = append(byParent[p.ppid], p)
	}
	var build func(pid int) []Process
	build = func(pid int) []Process {
		children := byParent[pid]
		out := make([]Process, 0, len(children))
		for _, c := range children {
			out = append(out, Process{PID: c.pid, PPID: c.ppid, Command: c.command, Children: build(c.pid)})
		}
		return out
	}
	return build(rootPID)
}
