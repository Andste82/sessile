package hosttools

import (
	"strings"
	"testing"
	"time"
)

// A command is spelled for the host it runs on: its working directory, a
// per-call environment that leaves nothing behind, and the devcontainer
// wrapper when the task asked for one (§4.12.3, §4.12.9).
func TestCommandSpelling(t *testing.T) {
	tests := []struct {
		name    string
		windows bool
		req     RunRequest
		want    []string
		not     []string
	}{
		{
			name: "plain command in a directory",
			req:  RunRequest{Command: "make test", Dir: "/home/ob/tasks/x/repo"},
			want: []string{"sh -lc ", `cd '\''/home/ob/tasks/x/repo'\''`, "make test"},
		},
		{
			name: "environment is set for this call only",
			req:  RunRequest{Command: "git push", Env: [][2]string{{"GH_TOKEN", "ghp_x"}}},
			want: []string{`export GH_TOKEN='\''ghp_x'\''`, "git push"},
		},
		{
			name: "a container command goes through devcontainer exec",
			req:  RunRequest{Command: "pytest -q", Container: true, Workspace: "/home/ob/tasks/x/repo"},
			want: []string{"devcontainer exec --workspace-folder", "pytest -q"},
		},
		{
			name:    "windows uses PowerShell",
			windows: true,
			req:     RunRequest{Command: "go build ./...", Dir: `C:\tasks\x`},
			want:    []string{"powershell -NoProfile -NonInteractive -Command", "Set-Location", "go build ./..."},
			not:     []string{"sh -lc"},
		},
		{
			name: "a quote in the command cannot break out",
			req:  RunRequest{Command: `echo 'it''s'; rm -rf /`, Dir: "/tmp"},
			want: []string{`'\''`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{windows: tt.windows}
			got := c.command(tt.req)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.not {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpected %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}

// Output is bounded and newest-kept: a chatty build must not fill the agent's
// context or the server's memory, and what it says at the end is what matters.
func TestBoundedWriterKeepsTheEnd(t *testing.T) {
	var live []byte
	w := &boundedWriter{limit: 10, onWrite: func(b []byte) { live = append(live, b...) }}
	for _, chunk := range []string{"aaaaa", "bbbbb", "ccccc"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got := w.String(); got != "bbbbbccccc" {
		t.Errorf("kept %q, want the last 10 bytes", got)
	}
	if !w.truncated {
		t.Error("truncation was not reported")
	}
	// Everything still reached the live stream, which is what the user watches.
	if string(live) != "aaaaabbbbbccccc" {
		t.Errorf("live output was %q", live)
	}
}

func TestRunRejectsAnEmptyCommand(t *testing.T) {
	c := &Client{}
	if _, err := c.Run(t.Context(), RunRequest{Command: "   "}); err == nil {
		t.Error("an empty command should be refused before it reaches the host")
	}
}

func TestDefaultsAndLimits(t *testing.T) {
	if DefaultRun <= 0 || MaxRunLimit < DefaultRun {
		t.Errorf("run timeouts are inconsistent: default %s, max %s", DefaultRun, MaxRunLimit)
	}
	if MaxOutput <= 0 || MaxRead < MaxOutput {
		t.Error("a file read should allow at least as much as one command's output")
	}
	if time.Duration(MaxRunLimit) > time.Hour {
		t.Error("a command may not outlive an hour")
	}
}
