package sshpty

import (
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// probeTimeout bounds both ProbeHostKey and the initial handshake inside
// Start — a host that never answers must not hang a request indefinitely.
const probeTimeout = 10 * time.Second

// ErrHostKeyUnknown is returned when a host has no pinned fingerprint yet
// (PROJECT_PLAN.md §4.5.1) — the first connection to it must prompt, never
// connect silently.
type ErrHostKeyUnknown struct {
	KeyType     string
	Fingerprint string
}

func (e *ErrHostKeyUnknown) Error() string {
	return fmt.Sprintf("host key not yet trusted: %s %s", e.KeyType, e.Fingerprint)
}

// ErrHostKeyChanged is returned when the presented key no longer matches the
// pinned fingerprint — could be a reinstalled server, could be something
// worse, and the caller must not decide which without the user.
type ErrHostKeyChanged struct {
	KeyType     string
	Fingerprint string
	Previous    string
}

func (e *ErrHostKeyChanged) Error() string {
	return fmt.Sprintf("host key changed: now %s %s, was %s", e.KeyType, e.Fingerprint, e.Previous)
}

// pinnedHostKeyCallback implements trust-on-first-use against trusted, the
// fingerprint currently pinned for this host ("" means not yet trusted).
// Never ssh.InsecureIgnoreHostKey() — see CLAUDE.md's security posture.
func pinnedHostKeyCallback(trusted string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		switch {
		case trusted == "":
			return &ErrHostKeyUnknown{KeyType: key.Type(), Fingerprint: fp}
		case fp != trusted:
			return &ErrHostKeyChanged{KeyType: key.Type(), Fingerprint: fp, Previous: trusted}
		default:
			return nil
		}
	}
}

// ProbeHostKey dials address and reports the key it presents, without
// authenticating. The host key is presented during the transport handshake,
// before user auth begins, so this works with no credentials — the
// connection is torn down the moment the key is captured, regardless of
// what authentication would have done next. Used by the Hosts page's
// "Verify host key" action and by the host-key/probe API endpoint.
func ProbeHostKey(address string) (keyType, fingerprint string, err error) {
	address = ensurePort(address)

	var captured ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: "sessile-probe", // never used — auth never runs
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			captured = key
			return nil // accept anything; we only want to read the key
		},
		Timeout: probeTimeout,
	}

	conn, err := net.DialTimeout("tcp", address, probeTimeout)
	if err != nil {
		return "", "", fmt.Errorf("dial %s: %w", address, err)
	}
	defer conn.Close()

	sshConn, _, _, handshakeErr := ssh.NewClientConn(conn, address, cfg)
	if sshConn != nil {
		defer sshConn.Close()
	}
	if captured != nil {
		return captured.Type(), ssh.FingerprintSHA256(captured), nil
	}
	// The handshake never got far enough to present a key at all.
	return "", "", fmt.Errorf("ssh handshake with %s: %w", address, handshakeErr)
}

// ensurePort appends the default SSH port if address doesn't already name
// one.
func ensurePort(address string) string {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return net.JoinHostPort(address, "22")
	}
	return address
}
