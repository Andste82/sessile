package session

import (
	"testing"
	"time"
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
