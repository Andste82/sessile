package sshpty

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/ssh"
)

// GitIdentity is what a host's own git setup says about the user (§4.16).
type GitIdentity struct {
	Name     string
	Email    string
	Username string
	Token    string
}

// gitHostRe is the only shape of git host the command below accepts.
var gitHostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}(:[0-9]{1,5})?$`)

const identityMarker = "@@SESSILE-GIT-IDENTITY@@"

// ReadGitIdentity reads the git identity a host already has (§4.16, "Import
// from host"): the global user.name and user.email, and whatever credential
// git's own helpers hold for https://gitHost. It is a fixed command with the
// validated git host as its only argument — a typed read, like ProcessTree
// (§4.10) — over a connection that checks the host's pinned key exactly like
// a session does. Nothing is written on the host, and prompts are disabled
// so a helper can't wait for input.
func ReadGitIdentity(target Target, gitHost string) (GitIdentity, error) {
	if !gitHostRe.MatchString(gitHost) {
		return GitIdentity{}, fmt.Errorf("%q is not a git host name", gitHost)
	}
	methods, err := authMethods(target)
	if err != nil {
		return GitIdentity{}, err
	}
	address := ensurePort(target.Address)
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            target.Username,
		Auth:            methods,
		HostKeyCallback: pinnedHostKeyCallback(target.TrustedHostKeyFingerprint),
		Timeout:         dialTimeout,
	})
	if err != nil {
		var unknown *ErrHostKeyUnknown
		if errors.As(err, &unknown) {
			return GitIdentity{}, unknown
		}
		var changed *ErrHostKeyChanged
		if errors.As(err, &changed) {
			return GitIdentity{}, changed
		}
		return GitIdentity{}, fmt.Errorf("ssh dial %s: %w", address, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return GitIdentity{}, fmt.Errorf("ssh new session: %w", err)
	}
	defer session.Close()
	var stdout bytes.Buffer
	session.Stdout = &stdout

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run(gitIdentityCommand(target.TargetOS, target.TerminalType, gitHost)) }()
	select {
	case <-done:
		// A non-zero exit is expected when nothing is stored for the host;
		// what was printed is still the answer.
	case <-ctx.Done():
		return GitIdentity{}, errors.New("reading the git identity timed out")
	}
	return parseGitIdentity(stdout.String()), nil
}

func gitIdentityCommand(targetOS, terminalType, gitHost string) string {
	if targetOS == "windows" || terminalType == "cmd" || terminalType == "powershell" {
		script := "git config --global user.name; '" + identityMarker + "'; git config --global user.email; '" + identityMarker + "'; " +
			"$env:GIT_TERMINAL_PROMPT='0'; $env:GCM_INTERACTIVE='never'; " +
			"\"protocol=https`nhost=" + gitHost + "`n`n\" | git credential fill"
		return "powershell -NoProfile -NonInteractive -EncodedCommand " + encodePS(script)
	}
	script := "git config --global user.name; echo '" + identityMarker + "'; git config --global user.email; echo '" + identityMarker + "'; " +
		"printf 'protocol=https\\nhost=%s\\n\\n' " + shellSingleQuote(gitHost) +
		" | GIT_TERMINAL_PROMPT=0 GCM_INTERACTIVE=never git credential fill 2>/dev/null"
	return "sh -c " + shellSingleQuote(script)
}

// encodePS is PowerShell's -EncodedCommand form: base64 of UTF-16LE, which
// no shell in between needs to quote.
func encodePS(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func parseGitIdentity(out string) GitIdentity {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	parts := strings.SplitN(out, identityMarker, 3)
	var id GitIdentity
	if len(parts) > 0 {
		id.Name = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		id.Email = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		for _, line := range strings.Split(parts[2], "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch k {
			case "username":
				id.Username = v
			case "password":
				id.Token = v
			}
		}
	}
	return id
}
