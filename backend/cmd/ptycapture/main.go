// Command ptycapture measures what a program leaves the terminal in while it is
// silent — the state §4.7's classifier samples.
//
// It drives a real pty, so what it reports is what a session would have seen:
// the program's own output, not a description of it. Temporary tooling for the
// capture the plan requires before a rule changes ("measured, not assumed").
//
//	go run ./cmd/ptycapture -for 12s -- docker run --rm -it ubuntu:24.04 bash
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

func main() {
	total := flag.Duration("for", 12*time.Second, "how long to run the program")
	quiet := flag.Duration("quiet", 2500*time.Millisecond, "silence before a sample is taken (waitQuiet)")
	send := flag.String("send", "", "bytes to type once the first output arrives (\\r for Enter)")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: ptycapture [flags] -- command [args…]")
		os.Exit(2)
	}

	cmd := exec.Command(flag.Arg(0), flag.Args()[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}

	var (
		mu sync.Mutex
		// A terminal starts with the cursor shown; a program that never touches
		// ?25 leaves it shown, which is exactly what a shell does.
		st         = modes{cursor: true}
		lastOutput = time.Now()
		bytesRead  int
	)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				mu.Lock()
				st.feed(buf[:n])
				lastOutput = time.Now()
				bytesRead += n
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	if *send != "" {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			io.WriteString(f, strings.ReplaceAll(*send, "\\r", "\r"))
		}()
	}

	fmt.Printf("== %s\n", strings.Join(flag.Args(), " "))
	deadline := time.Now().Add(*total)
	reported := false
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		mu.Lock()
		silent := time.Since(lastOutput)
		snap, n := st, bytesRead
		mu.Unlock()
		switch {
		case silent >= *quiet && !reported:
			fmt.Printf("   quiet %4.1fs after %d bytes → %s\n", silent.Seconds(), n, snap)
			reported = true
		case silent < *quiet:
			reported = false // output resumed; report the next lull too
		}
	}

	cmd.Process.Kill()
	f.Close()
	cmd.Wait()

	mu.Lock()
	defer mu.Unlock()
	fmt.Printf("   final state → %s\n\n", st)
}

// modes is the subset of terminal state the classifier cares about, tracked the
// way vtScanner tracks it: a byte-wise state machine, no screen, no content.
type modes struct {
	st      int // 0 ground, 1 esc, 2 csi, 3 string, 4 string+esc
	params  []byte
	cursor  bool // ?25
	paste   bool // ?2004
	alt     bool // ?1049 / 47 / 1047
	mouse   bool // ?1000 / 1002 / 1003
	focus   bool // ?1004
	marks   bool // any OSC 133 seen
	atPromt bool // last OSC 133 said prompt
	osc     []byte
	inOSC   bool
	bells   int
}

func (m *modes) feed(p []byte) {
	for _, b := range p {
		switch m.st {
		case 0:
			if b == 0x1b {
				m.st = 1
			} else if b == 0x07 {
				m.bells++
			}
		case 1:
			switch b {
			case '[':
				m.st, m.params = 2, m.params[:0]
			case ']':
				m.st, m.inOSC, m.osc = 3, true, m.osc[:0]
			case 'P', 'X', '^', '_':
				m.st, m.inOSC = 3, false
			case 0x1b:
				m.st = 1
			default:
				m.st = 0
			}
		case 2:
			switch {
			case b >= 0x40 && b <= 0x7e:
				m.csi(b)
				m.st = 0
			case b >= 0x20 && b <= 0x3f:
				if len(m.params) < 64 {
					m.params = append(m.params, b)
				}
			default:
				m.st = 0
			}
		case 3:
			switch b {
			case 0x07:
				m.endString()
			case 0x1b:
				m.st = 4
			default:
				if m.inOSC && len(m.osc) < 16 {
					m.osc = append(m.osc, b)
				}
			}
		case 4:
			if b == '\\' {
				m.endString()
			} else {
				m.inOSC = false
				m.st = 1
				m.feed([]byte{b})
			}
		}
	}
}

func (m *modes) endString() {
	if m.inOSC && strings.HasPrefix(string(m.osc), "133;") && len(m.osc) > 4 {
		m.marks = true
		m.atPromt = m.osc[4] != 'C'
	}
	m.st, m.inOSC = 0, false
}

func (m *modes) csi(final byte) {
	if final != 'h' && final != 'l' {
		return
	}
	if len(m.params) == 0 || m.params[0] != '?' {
		return
	}
	set := final == 'h'
	for _, ps := range strings.Split(string(m.params[1:]), ";") {
		switch ps {
		case "25":
			m.cursor = set
		case "2004":
			m.paste = set
		case "47", "1047", "1049":
			m.alt = set
		case "1000", "1002", "1003":
			m.mouse = set
		case "1004":
			m.focus = set
		}
	}
}

func (m modes) String() string {
	onOff := func(b bool) string {
		if b {
			return "on "
		}
		return "off"
	}
	marks := "none"
	if m.marks {
		marks = "at-prompt"
		if !m.atPromt {
			marks = "running"
		}
	}
	return fmt.Sprintf("cursor=%s paste=%s alt=%s mouse=%s focus=%s marks=%s bells=%d",
		map[bool]string{true: "shown ", false: "hidden"}[m.cursor],
		onOff(m.paste), onOff(m.alt), onOff(m.mouse), onOff(m.focus), marks, m.bells)
}
