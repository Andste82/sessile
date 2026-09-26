//go:build linux

// Package confine restricts what a process sessile starts may touch on the
// sessile server (PROJECT_PLAN.md §4.12.9, E13/E14, v0.9).
//
// It matters because v0.9 moved agents here. An agent is a program that
// follows text it reads — a ticket description, a README, a CI log — and it
// runs as sessile's own OS user, which can read --data-dir: every user's
// hosts.yml with its plaintext SSH credentials, every user's agent tokens,
// every script's settings. Confinement is what stops one injected
// instruction from reaching all of that. The same applies to user scripts,
// which have always run here (§11).
//
// Linux Landlock does it with no root, no container and no new dependency:
// a ruleset naming the few paths the process may use, applied just before
// exec, inherited by everything it spawns, and impossible to drop
// afterwards. What it does not do is stop network egress — an agent must
// reach its vendor's API, so it can reach anywhere. What it removes is
// access to other people's secrets to send there.
package confine

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Access sets, as Landlock ABI 1 defines them. Sessile asks for no more than
// ABI 1 so that every kernel from 5.13 on can enforce the same rules.
const (
	accessFSExecute    = unix.LANDLOCK_ACCESS_FS_EXECUTE
	accessFSWriteFile  = unix.LANDLOCK_ACCESS_FS_WRITE_FILE
	accessFSReadFile   = unix.LANDLOCK_ACCESS_FS_READ_FILE
	accessFSReadDir    = unix.LANDLOCK_ACCESS_FS_READ_DIR
	accessFSRemoveDir  = unix.LANDLOCK_ACCESS_FS_REMOVE_DIR
	accessFSRemoveFile = unix.LANDLOCK_ACCESS_FS_REMOVE_FILE
	accessFSMakeChar   = unix.LANDLOCK_ACCESS_FS_MAKE_CHAR
	accessFSMakeDir    = unix.LANDLOCK_ACCESS_FS_MAKE_DIR
	accessFSMakeReg    = unix.LANDLOCK_ACCESS_FS_MAKE_REG
	accessFSMakeSock   = unix.LANDLOCK_ACCESS_FS_MAKE_SOCK
	accessFSMakeFifo   = unix.LANDLOCK_ACCESS_FS_MAKE_FIFO
	accessFSMakeBlock  = unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK
	accessFSMakeSym    = unix.LANDLOCK_ACCESS_FS_MAKE_SYM

	// readOnly is everything a process needs to read a tree and run programs
	// from it; readWrite adds creating, changing and removing.
	readOnly  = accessFSExecute | accessFSReadFile | accessFSReadDir
	readWrite = readOnly | accessFSWriteFile | accessFSRemoveDir | accessFSRemoveFile |
		accessFSMakeChar | accessFSMakeDir | accessFSMakeReg | accessFSMakeSock |
		accessFSMakeFifo | accessFSMakeBlock | accessFSMakeSym

	// handled is the whole of ABI 1: everything not granted below is denied.
	handled = readWrite
)

// rulesetAttr is struct landlock_ruleset_attr.
type rulesetAttr struct {
	HandledAccessFS uint64
}

// pathBeneathAttr is struct landlock_path_beneath_attr.
type pathBeneathAttr struct {
	AllowedAccess uint64
	ParentFd      int32
	_             [4]byte // padding to the kernel's 8-byte alignment
}

// Rules are the paths a confined process may use. Everything else on the
// filesystem is denied — including --data-dir, which is the point.
type Rules struct {
	// ReadWrite are the trees it owns: its task folder, its temp dir.
	ReadWrite []string
	// ReadOnly are the trees it needs but must not change: the agent CLI,
	// its runtime, the system libraries and certificates.
	ReadOnly []string
}

// ErrUnsupported is returned where Landlock is not available: an older
// kernel, or one built without it. Sessile refuses to start a confined
// process rather than silently starting an unconfined one (E14).
var ErrUnsupported = errors.New("this kernel has no Landlock support (Linux 5.13+ with landlock enabled)")

// Supported reports whether this kernel can enforce the rules, without
// changing anything about the current process.
func Supported() bool {
	abi, err := createRuleset(nil, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if err != nil {
		return false
	}
	// The call returns the ABI version, not a descriptor, in version mode.
	_ = abi
	return true
}

// Apply restricts the calling thread — and everything it later execs — to
// the rules. It is irreversible by design: a confined process cannot widen
// its own rules, so an agent cannot talk its way out of them.
//
// Call it between fork and exec, on the child's thread only — with
// runtime.LockOSThread held, because the restriction lands on the calling
// thread and the exec has to happen on that same one. Applying it on a
// server thread would confine sessile itself.
func Apply(r Rules) error {
	if !Supported() {
		return ErrUnsupported
	}
	attr := rulesetAttr{HandledAccessFS: handled}
	fd, err := createRuleset(&attr, 0)
	if err != nil {
		return fmt.Errorf("create the ruleset: %w", err)
	}
	defer syscall.Close(fd)

	for _, p := range r.ReadWrite {
		if err := allow(fd, p, readWrite); err != nil {
			return err
		}
	}
	for _, p := range r.ReadOnly {
		if err := allow(fd, p, readOnly); err != nil {
			return err
		}
	}
	// no_new_privs first: the kernel refuses to restrict a process that
	// could still gain privileges through a setuid binary.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(fd), 0, 0); errno != 0 {
		return fmt.Errorf("apply the ruleset: %w", errno)
	}
	return nil
}

// allow grants access beneath one path. A path that does not exist is
// skipped rather than fatal: the rules name places that are normal to have
// (/usr/lib64, a distribution's certificate directory) and normal to lack.
func allow(rulesetFd int, path string, access uint64) error {
	fd, err := syscall.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer syscall.Close(fd)

	rule := pathBeneathAttr{AllowedAccess: access, ParentFd: int32(fd)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFd),
		uintptr(unix.LANDLOCK_RULE_PATH_BENEATH), uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("allow %s: %w", path, errno)
	}
	return nil
}

func createRuleset(attr *rulesetAttr, flags uintptr) (int, error) {
	var ptr uintptr
	var size uintptr
	if attr != nil {
		ptr = uintptr(unsafe.Pointer(attr))
		size = unsafe.Sizeof(*attr)
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, ptr, size, flags)
	if errno != 0 {
		return 0, errno
	}
	return int(fd), nil
}
