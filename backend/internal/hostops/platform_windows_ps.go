package hostops

import (
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

// windowsPlatform lists processes with a PowerShell one-liner. It invokes
// `powershell` explicitly rather than relying on the remote's default shell
// — Win32-OpenSSH defaults new sessions to cmd.exe unless reconfigured, so
// Exec's line must launch PowerShell itself, not assume it's already there.
type windowsPlatform struct{}

// NewWindowsPlatform returns a Platform for a Windows target.
func NewWindowsPlatform() Platform { return windowsPlatform{} }

const windowsProcessListScript = `powershell -NoProfile -NonInteractive -Command "Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name | ConvertTo-Csv -NoTypeInformation"`

func (windowsPlatform) ProcessTree(ctx context.Context, t Transport, rootPID *int) ([]Process, error) {
	res, err := t.Exec(ctx, windowsProcessListScript)
	if err != nil {
		return nil, fmt.Errorf("powershell: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("powershell exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	flat, err := parseWindowsProcessCSV(string(res.Stdout))
	if err != nil {
		return nil, fmt.Errorf("parse powershell output: %w", err)
	}
	if rootPID == nil {
		// Windows has no equivalent of Linux's "1" (init) convention — no
		// single well-known ancestor of everything — so "the whole
		// target's tree" means a forest here, not a rooted walk.
		return buildProcessForest(flat), nil
	}
	return buildProcessTree(flat, *rootPID), nil
}

// parseWindowsProcessCSV reads ConvertTo-Csv's output: a header row
// ("ProcessId","ParentProcessId","Name") followed by one quoted-CSV row per
// process. encoding/csv, not a manual split, because a process name can in
// principle contain a comma or a quote and ConvertTo-Csv escapes those
// per RFC 4180.
func parseWindowsProcessCSV(output string) ([]flatProcess, error) {
	r := csv.NewReader(strings.NewReader(output))
	r.FieldsPerRecord = -1 // tolerate a trailing blank line
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var out []flatProcess
	for i, rec := range records {
		if i == 0 || len(rec) < 3 { // header row, or a short/blank line
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rec[0]))
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err != nil {
			continue
		}
		out = append(out, flatProcess{pid: pid, ppid: ppid, command: rec[2]})
	}
	return out, nil
}
