//go:build !linux

package confine

import "errors"

// Rules are the paths a confined process may use (see the Linux build).
type Rules struct {
	ReadWrite []string
	ReadOnly  []string
}

// ErrUnsupported is returned everywhere but Linux: Landlock is a Linux LSM,
// and sessile refuses to start a confined process rather than pretending
// (E14).
var ErrUnsupported = errors.New("agent confinement needs Linux with Landlock (5.13+)")

// Supported is false off Linux.
func Supported() bool { return false }

// Apply always fails off Linux.
func Apply(Rules) error { return ErrUnsupported }
