package session

import (
	"syscall"

	"github.com/Andste82/sessile/backend/internal/terminal"
)

// Backend is what a Session actually reads from and writes to: a local PTY
// today (*terminal.PTY), and — once §12b M17 lands — an SSH-backed shell
// too. Every other piece of the session/read-loop/broadcast/scrollback
// machinery in this package is written against this interface, not against
// *terminal.PTY directly, so neither has to change when a second
// implementation arrives.
type Backend interface {
	Read(p []byte) (int, error)
	Write(data []byte) error
	Resize(rows, cols uint16) error
	Pid() int // 0 where "process id" has no meaning (a remote shell)
	Signal(sig syscall.Signal)
	Wait()
	CloseFile()
	Foreground() terminal.Foreground // zero value where it cannot be determined (§4.7)
}
