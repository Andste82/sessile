package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/terminal"
)

// The §4.7 rule table, one case per row plus the boundaries between them. This
// is the whole reason classify takes a snapshot rather than reading the session:
// the interesting behaviour is decidable without a PTY, a clock or a lock.
func TestClassify(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	tests := []struct {
		name string
		in   activityInput
		want Activity
	}{
		// Rule 1 — bytes are arriving.
		{
			name: "output just now",
			in:   activityInput{lastOutput: ago(100 * time.Millisecond), fg: fgProgram},
			want: ActivityBusy,
		},
		{
			name: "output beats a prompt that is also showing",
			in:   activityInput{lastOutput: ago(100 * time.Millisecond), bracketedPaste: true, fg: fgShell},
			want: ActivityBusy,
		},
		{
			name: "htop repaints every 1.5s and stays busy",
			in:   activityInput{lastOutput: ago(busyWindow - time.Millisecond), fg: fgProgram},
			want: ActivityBusy,
		},

		// Rule 2 — a line is being read and it is not a program's.
		{
			name: "shell at its prompt",
			in:   activityInput{lastOutput: ago(time.Minute), bracketedPaste: true, fg: fgShell},
			want: ActivityIdle,
		},
		{
			// Off Linux the foreground process cannot be identified. A prompt is
			// the likelier reading and the quieter one to be wrong about, so such
			// a build never invents a question.
			name: "unknown foreground reading a line is idle, not waiting",
			in:   activityInput{lastOutput: ago(time.Minute), bracketedPaste: true, fg: fgUnknown},
			want: ActivityIdle,
		},

		{
			// `bash` typed at the prompt is at a prompt, exactly like the shell
			// that started it. Only its pid says otherwise, and the pid is not
			// what the rule is about.
			name: "a nested shell at its prompt",
			in:   activityInput{lastOutput: ago(time.Minute), bracketedPaste: true, fg: fgNestedShell},
			want: ActivityIdle,
		},

		// Rule 3 — a program is asking.
		{
			name: "claude code waiting for input",
			in:   activityInput{lastOutput: ago(10 * time.Second), bracketedPaste: true, fg: fgProgram},
			want: ActivityWaiting,
		},
		{
			name: "program reading a line but not quiet long enough yet",
			in:   activityInput{lastOutput: ago(waitQuiet - time.Millisecond), bracketedPaste: true, fg: fgProgram},
			want: ActivityBusy,
		},
		{
			name: "program reading a line exactly at the dwell time",
			in:   activityInput{lastOutput: ago(waitQuiet), bracketedPaste: true, fg: fgProgram},
			want: ActivityWaiting,
		},

		// Rule 4 — the shell owns the terminal but announced no line editor.
		{
			name: "dash has no readline and is still at its prompt",
			in:   activityInput{lastOutput: ago(time.Minute), fg: fgShell},
			want: ActivityIdle,
		},

		{
			// `bash deploy.sh` keeps the name and loses the line editor. This is
			// the case that keeps the nested-shell rule from turning every long
			// script into "idle at the prompt".
			name: "a shell running a script is working, not prompting",
			in:   activityInput{lastOutput: ago(time.Minute), fg: fgNestedShell},
			want: ActivityBusy,
		},

		// Rule 5 — a bell asks to be looked at.
		{
			name: "recent bell from a program",
			in:   activityInput{lastOutput: ago(time.Minute), lastBell: ago(5 * time.Second), fg: fgProgram},
			want: ActivityWaiting,
		},
		{
			name: "stale bell is not attention any more",
			in:   activityInput{lastOutput: ago(time.Hour), lastBell: ago(bellWindow), fg: fgProgram},
			want: ActivityBusy,
		},
		{
			name: "bell does not override the shell's own prompt",
			in:   activityInput{lastOutput: ago(time.Minute), lastBell: ago(time.Second), fg: fgShell},
			want: ActivityIdle,
		},

		// Rule 6 — running, silent, not prompting.
		{
			name: "quiet go build is working, not asking",
			in:   activityInput{lastOutput: ago(3 * time.Minute), fg: fgProgram},
			want: ActivityBusy,
		},
		{
			// python3's input() sets no bracketed paste and rings no bell. It
			// reads as busy: a false negative, which is the direction to err in.
			name: "program prompting without announcing it stays busy",
			in:   activityInput{lastOutput: ago(30 * time.Second), fg: fgProgram},
			want: ActivityBusy,
		},
		{
			name: "nothing known at all",
			in:   activityInput{lastOutput: ago(time.Hour), fg: fgUnknown},
			want: ActivityBusy,
		},

		// Rule 0 — prompt marks, where a shell emits them. These are the cases
		// the foreground lookup cannot reach: the shell is on the far side of a
		// tmux pane, a container or an ssh hop, so our own pty reports the
		// wrapper as the foreground program and the shell's bracketed paste
		// arrives anyway. Without the marks every one of these reads as waiting.
		{
			name: "a marked prompt behind a wrapper is idle, not a question",
			in: activityInput{
				lastOutput: ago(time.Minute), bracketedPaste: true, fg: fgProgram,
				promptMarks: true, atPrompt: true,
			},
			want: ActivityIdle,
		},
		{
			name: "a marked prompt still redrawing is busy",
			in: activityInput{
				lastOutput: ago(100 * time.Millisecond), bracketedPaste: true, fg: fgProgram,
				promptMarks: true, atPrompt: true,
			},
			want: ActivityBusy,
		},
		{
			// The marks say a command is running; they never say what it wants.
			// Bracketed paste still decides, which is what keeps Claude Code in
			// a container reporting the one state it exists for.
			name: "a program behind a wrapper still asks",
			in: activityInput{
				lastOutput: ago(10 * time.Second), bracketedPaste: true, fg: fgProgram,
				promptMarks: true, atPrompt: false,
			},
			want: ActivityWaiting,
		},
		{
			name: "a marked command running quietly is working",
			in: activityInput{
				lastOutput: ago(time.Minute), fg: fgProgram,
				promptMarks: true, atPrompt: false,
			},
			want: ActivityBusy,
		},
		{
			// "A command is running" must not outvote the kernel: an interactive
			// `bash` started from a shell with integration *is* a command that
			// shell ran, and it is also a prompt. The lookup can see that; the
			// mark cannot.
			name: "a marked command does not outrank a shell in the foreground",
			in: activityInput{
				lastOutput: ago(time.Minute), bracketedPaste: true, fg: fgNestedShell,
				promptMarks: true, atPrompt: false,
			},
			want: ActivityIdle,
		},
		{
			// Where the lookup answers nothing at all, the mark is all there is,
			// and it says the foreground is not a shell.
			name: "a marked command fills in an unidentifiable foreground",
			in: activityInput{
				lastOutput: ago(10 * time.Second), bracketedPaste: true, fg: fgUnknown,
				promptMarks: true, atPrompt: false,
			},
			want: ActivityWaiting,
		},
		{
			// A shell with no integration emits nothing, and that must stay
			// distinct from "not at a prompt" — otherwise every unmarked shell
			// would be read as running a command forever.
			name: "no marks at all falls back to the foreground lookup",
			in: activityInput{
				lastOutput: ago(time.Minute), bracketedPaste: true, fg: fgShell,
				promptMarks: false, atPrompt: false,
			},
			want: ActivityIdle,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(now, tt.in); got != tt.want {
				t.Errorf("classify = %q, want %q", got, tt.want)
			}
		})
	}
}

// Leaving "waiting" needs sustained output, not a byte. This is the difference
// between a dashboard you can glance at and one that flickers: a real Claude
// Code session sitting at its prompt repaints every few seconds, and without
// the hysteresis each repaint dropped the indicator to busy for four seconds.
func TestWaitingSurvivesARepaint(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	waiting := activityInput{
		previous:       ActivityWaiting,
		lastOutput:     now, // the repaint landed this instant
		bracketedPaste: true,
		fg:             fgProgram,
	}

	t.Run("an isolated repaint does not count as work", func(t *testing.T) {
		if got := classify(now, waiting); got != ActivityWaiting {
			t.Errorf("classify = %q, want %q", got, ActivityWaiting)
		}
	})

	t.Run("sustained output does", func(t *testing.T) {
		in := waiting
		in.sustainedOutput = true
		if got := classify(now, in); got != ActivityBusy {
			t.Errorf("classify = %q, want %q", got, ActivityBusy)
		}
	})

	t.Run("the program exiting releases it", func(t *testing.T) {
		in := waiting
		in.fg = fgShell
		if got := classify(now.Add(time.Minute), in); got != ActivityIdle {
			t.Errorf("classify = %q, want %q", got, ActivityIdle)
		}
	})

	t.Run("the line editor closing releases it", func(t *testing.T) {
		in := waiting
		in.bracketedPaste = false
		if got := classify(now.Add(time.Minute), in); got != ActivityBusy {
			t.Errorf("classify = %q, want %q", got, ActivityBusy)
		}
	})

	t.Run("a prompt mark releases it", func(t *testing.T) {
		// The program behind the wrapper exited and the shell drew its prompt.
		// Nothing our own pty can see changed — the wrapper is still the
		// foreground program and its line editor is still open — so the mark is
		// the only thing that can end the state.
		in := waiting
		in.promptMarks, in.atPrompt = true, true
		if got := classify(now.Add(time.Minute), in); got != ActivityIdle {
			t.Errorf("classify = %q, want %q", got, ActivityIdle)
		}
	})

	t.Run("a mark saying a command is running does not", func(t *testing.T) {
		in := waiting
		in.promptMarks, in.atPrompt = true, false
		if got := classify(now, in); got != ActivityWaiting {
			t.Errorf("classify = %q, want %q", got, ActivityWaiting)
		}
	})

	t.Run("a nested shell taking over releases it", func(t *testing.T) {
		in := waiting
		in.fg = fgNestedShell
		if got := classify(now.Add(time.Minute), in); got != ActivityIdle {
			t.Errorf("classify = %q, want %q", got, ActivityIdle)
		}
	})

	t.Run("the hysteresis does not apply to other states", func(t *testing.T) {
		in := waiting
		in.previous = ActivityIdle
		if got := classify(now, in); got != ActivityBusy {
			t.Errorf("classify = %q, want %q — only waiting is sticky", got, ActivityBusy)
		}
	})
}

// The counter is about continuity, not volume: two samples in a row with any
// output at all, and none the moment output stops.
func TestSampleOutputRun(t *testing.T) {
	var s Session

	if s.sampleOutputRunLocked() {
		t.Error("a session that has produced nothing reports sustained output")
	}

	s.outputBytes += 10
	if s.sampleOutputRunLocked() {
		t.Error("one sample with output is not sustained")
	}
	s.outputBytes += 1 // a single byte is enough to continue a run
	if !s.sampleOutputRunLocked() {
		t.Error("two samples in a row with output should be sustained")
	}

	if s.sampleOutputRunLocked() {
		t.Error("a sample with no new output must end the run immediately")
	}
}

// A zero lastBell is "no bell ever", not "a bell in 1970" — and the zero time is
// far enough in the past that an unguarded comparison would go the right way by
// accident. Pin it so a later refactor cannot break it silently.
func TestClassifyTreatsAZeroBellAsNoBell(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	in := activityInput{lastOutput: now.Add(-time.Hour), fg: fgProgram}
	if got := classify(now, in); got != ActivityBusy {
		t.Errorf("classify with no bell = %q, want %q", got, ActivityBusy)
	}
}

// The sampler runs against a live shell: the session must settle on idle at its
// prompt and name the shell as the foreground program.
func TestSampleActivityReportsARealSession(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.Create("probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(info.ID) })

	// sh prints no prompt to a pipe-quiet pty, so the settling time is just the
	// busy window elapsing after the shell's startup output.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mgr.sampleActivity()
		got, err := mgr.Get(info.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Activity == ActivityIdle && got.Command == "sh" {
			if got.ActivitySince.IsZero() {
				t.Error("activitySince not stamped on the transition")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never settled: activity=%q command=%q cwd=%q",
				got.Activity, got.Command, got.Cwd)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The label is the one place the chain is seen, so its shape is pinned here.
func TestCommandLabel(t *testing.T) {
	tests := []struct {
		name string
		fg   terminal.Foreground
		want string
	}{
		{"nothing known", terminal.Foreground{}, ""},
		{"no chain, only a leader", terminal.Foreground{Name: "claude"}, "claude"},
		{"a program started directly", terminal.Foreground{Name: "claude", Chain: []string{"claude"}}, "claude"},
		{
			"a script and what it runs",
			terminal.Foreground{Name: "bash", Chain: []string{"bash", "ping"}},
			"bash › ping",
		},
		{
			"at the cap, still whole",
			terminal.Foreground{Name: "bash", Chain: []string{"bash", "make", "cc"}},
			"bash › make › cc",
		},
		{
			// Past the cap the ends carry the meaning: what was started, and
			// what is running now.
			"past the cap, ends only",
			terminal.Foreground{Name: "bash", Chain: []string{"bash", "make", "sh", "cc", "cc1plus"}},
			"bash › … › cc1plus",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandLabel(tt.fg); got != tt.want {
				t.Errorf("commandLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

// The names that make a foreground process a shell: the built-in set, plus
// whatever the operator allowed, and nothing else.
func TestIsShellName(t *testing.T) {
	m := &Manager{shells: []string{"sh", "/usr/local/bin/nu"}}

	tests := []struct {
		name string
		want bool
	}{
		{"bash", true},
		{"zsh", true},
		{"fish", true},
		{"dash", true},
		// Configured by the operator, absent from the built-in set. Matched by
		// base name so an allowlist entry may be a path.
		{"nu", true},
		{"claude", false},
		{"htop", false},
		{"docker", false},
		{"tmux: client", false},
		// A lookup that lost its race with an exiting process tells us nothing,
		// and "nothing" must not become "a shell at its prompt".
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.isShellName(tt.name); got != tt.want {
				t.Errorf("isShellName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// End to end against a real nested shell: the whole point of the change, and
// the one part the pure function cannot prove. Before it, this session reported
// "waiting for input" for as long as the nested shell sat at its prompt.
func TestSampleActivityReadsANestedShellAsAPrompt(t *testing.T) {
	mgr, _, _ := testManager(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	info, err := mgr.Create("probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(info.ID) })

	// bash rather than sh: a nested dash announces no line editor and so cannot
	// be told from a running script, which is the documented limit of §4.7.
	// Not `exec` either — that would reuse the pid and so still be the session's
	// own shell, which is the case that already worked.
	if err := mgr.WriteInput(info.ID, []byte("bash --norc -i\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The foreground name reaching "bash" is itself part of the assertion: it
	// can only be a process group other than the session shell's.
	waitForActivity(t, mgr, info.ID, func(i Info) bool {
		return i.Command == "bash" && i.Activity == ActivityIdle
	})
}

// End to end for the chain: a script is not what a session is doing, it is what
// started what the session is doing. Before this, such a session reported
// "bash" and said nothing about the program the user was actually waiting on.
func TestSampleActivityNamesTheProgramInsideAScript(t *testing.T) {
	mgr, _, _ := testManager(t)
	// Outside the sandbox root on purpose: the path is one the user typed, and
	// §4.5 governs where a session may be *created*, not what it may run.
	script := filepath.Join(t.TempDir(), "work.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 20\n:\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	info, err := mgr.Create("probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(info.ID) })

	if err := mgr.WriteInput(info.ID, []byte("/bin/sh "+script+"\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := waitForActivity(t, mgr, info.ID, func(i Info) bool {
		return i.Command == "sh › sleep"
	})
	// And the state follows the leaf, not the script: a shell running a script
	// is work, and must not be read as a shell at a prompt.
	if got.Activity != ActivityBusy {
		t.Errorf("activity %q while a script runs, want %q", got.Activity, ActivityBusy)
	}
}

// End to end for the prompt marks, through a foreground program the lookup
// cannot see into — the shape of `docker run -it`, without needing docker.
//
// The two cases differ by one letter of one escape sequence and must come out
// opposite: the same bytes, the same silence, the same unknown program in the
// foreground. That is the whole claim of input 4, and nothing else in the
// session can distinguish them.
func TestSampleActivityFollowsPromptMarks(t *testing.T) {
	wrapper := fakeWrapper(t)

	tests := []struct {
		name string
		mark string
		want Activity
	}{
		// A shell is at its prompt on the far side. Without the mark this is
		// rule 3 — a program, a line editor, silence — and reads as a question.
		{"a marked prompt behind the wrapper", "A", ActivityIdle},
		// A command is running there, and it is holding a line editor open. The
		// mark says nothing about what it wants, so rule 3 still decides: this
		// is Claude Code in a container, and it must keep asking.
		{"a marked command behind the wrapper", "C", ActivityWaiting},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, _, _ := testManager(t)
			info, err := mgr.Create("probe", ".", "sh")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			t.Cleanup(func() { _ = mgr.Delete(info.ID) })

			// Bracketed paste and the mark in one write, then a builtin read so
			// the wrapper stays in the foreground under its own name — a shell
			// that exec'd away would change the name and reset the marks, which
			// is a different test.
			sent := time.Now()
			cmd := wrapper + " -c 'printf \"\\033[?2004h\\033]133;" + tt.mark + "\\007\"; read x'\n"
			if err := mgr.WriteInput(info.ID, []byte(cmd)); err != nil {
				t.Fatalf("write: %v", err)
			}

			if got := settled(t, mgr, info.ID, sent); got.Activity != tt.want {
				t.Fatalf("activity %q with mark %q, want %q (foreground %q)",
					got.Activity, tt.mark, tt.want, got.Command)
			}
		})
	}
}

// What the marks said dies with the program that said it. Without this, a
// container shell's last mark outlives the container: the session's own shell
// emits none of its own, so nothing can ever contradict it, and the session
// keeps answering for a program that exited.
func TestSampleActivityForgetsMarksWhenTheForegroundChanges(t *testing.T) {
	wrapper := fakeWrapper(t)
	mgr, _, _ := testManager(t)
	info, err := mgr.Create("probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(info.ID) })

	// A wrapper says "a shell is at its prompt here" and then exits.
	sent := time.Now()
	if err := mgr.WriteInput(info.ID, []byte(wrapper+" -c 'printf \"\\033]133;A\\007\"'\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := settled(t, mgr, info.ID, sent); got.Activity != ActivityIdle {
		t.Fatalf("activity %q back at the session's own prompt, want %q", got.Activity, ActivityIdle)
	}

	// A different program now holds a line editor open and says nothing. If the
	// mark survived its author, this reads as a prompt that is not there.
	sent = time.Now()
	if err := mgr.WriteInput(info.ID, []byte(wrapper+" -c 'printf \"\\033[?2004h\"; read x'\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := settled(t, mgr, info.ID, sent); got.Activity != ActivityWaiting {
		t.Fatalf("activity %q under a stale mark, want %q", got.Activity, ActivityWaiting)
	}
}

// fakeWrapper is a copy of sh under a name that is not a shell's, so the
// foreground lookup classifies it as a program — what `docker`, `ssh` and
// `tmux` all are to us. Copied rather than symlinked: the kernel takes comm
// from the file that was executed.
func fakeWrapper(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	body, err := os.ReadFile(sh)
	if err != nil {
		t.Skipf("cannot read %s: %v", sh, err)
	}
	path := filepath.Join(t.TempDir(), "wrapper")
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	return path
}

// settled waits for output written after sent to arrive and then go quiet for
// longer than every window in §4.7, and returns the session's state then.
//
// Both halves are load-bearing. Polling for a state would prove nothing —
// writing to a pty produces an echo, the echo is output, and output alone is
// rule 1, so a test that accepted the first "busy" it saw would pass against a
// build that ignores prompt marks entirely. And without the arrival check it
// returns the *previous* quiet state, which is a stale reading of a session
// that has already gone quiet once.
func settled(t *testing.T, m *Manager, id string, sent time.Time) Info {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		// Sampled all the way through, not just at the end: a foreground program
		// that comes and goes between two samples is one the sampler never saw,
		// and half of what is being tested here is what it does when it changes.
		m.sampleActivity()
		got, err := m.Get(id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.LastActivity.After(sent) && time.Since(got.LastActivity) > waitQuiet+busyWindow {
			m.sampleActivity()
			got, err = m.Get(id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never went quiet: activity=%q", got.Activity)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForActivity samples until ok accepts the session's state, and fails the
// test with what it last saw. The sampler is driven by hand rather than by its
// ticker so the test is not racing a one-second interval.
func waitForActivity(t *testing.T, m *Manager, id string, ok func(Info) bool) Info {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last Info
	for {
		m.sampleActivity()
		got, err := m.Get(id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		last = got
		if ok(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never settled: activity=%q command=%q", last.Activity, last.Command)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A stopped session must not keep advertising the program it was running.
func TestSampleActivityClearsAStoppedSession(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.Create("probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.sampleActivity()

	if err := mgr.WriteInput(info.ID, []byte("exit\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mgr.sampleActivity()
		got, err := mgr.Get(info.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Status == StatusStopped {
			if got.Activity != "" || got.Command != "" || got.Cwd != "" {
				t.Errorf("stopped session still reports activity=%q command=%q cwd=%q",
					got.Activity, got.Command, got.Cwd)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("session never stopped")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
