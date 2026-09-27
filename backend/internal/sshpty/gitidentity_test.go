package sshpty

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestParseGitIdentity(t *testing.T) {
	out := "Sess Test\n" + identityMarker + "\nst@example.com\r\n" + identityMarker +
		"\nprotocol=https\nhost=github.com\nusername=octo\npassword=ghp_x=y\n"
	got := parseGitIdentity(out)
	want := GitIdentity{Name: "Sess Test", Email: "st@example.com", Username: "octo", Token: "ghp_x=y"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got := parseGitIdentity(""); got != (GitIdentity{}) {
		t.Fatalf("empty output: %+v", got)
	}
}

func TestGitIdentityCommandQuotesTheHost(t *testing.T) {
	posix := gitIdentityCommand("linux", "bash", "github.com")
	if !strings.HasPrefix(posix, "sh -c '") || !strings.Contains(posix, `'\''github.com'\''`) {
		t.Errorf("posix command = %s", posix)
	}
	win := gitIdentityCommand("windows", "powershell", "github.com")
	enc := strings.TrimPrefix(win, "powershell -NoProfile -NonInteractive -EncodedCommand ")
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("not an encoded command: %s", win)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if s := string(utf16.Decode(u)); !strings.Contains(s, "host=github.com") || !strings.Contains(s, "GCM_INTERACTIVE='never'") {
		t.Errorf("decoded = %s", s)
	}
	if _, err := ReadGitIdentity(Target{}, "x;rm -rf /"); err == nil {
		t.Error("an invalid git host must be refused before anything dials")
	}
}
