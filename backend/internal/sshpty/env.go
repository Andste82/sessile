package sshpty

import (
	"errors"
	"io"
	"os"
	"path"

	"github.com/pkg/sftp"

	"github.com/Andste82/sessile/backend/internal/envfile"
)

// SupportsEnv reports whether a target can take a start directory and
// environment variables: both are POSIX `sh` syntax in the exec preamble
// (wrapWithPIDRecording). Either signal alone means the remote side is not
// a POSIX shell — targetOS because a Windows host can run a "custom"
// command, terminalType because targetOS is informational and often "".
// This is the one place the rule lives.
func SupportsEnv(targetOS, terminalType string) bool {
	return !(targetOS == "windows" || terminalType == "cmd" || terminalType == "powershell")
}

var (
	// ErrEnvUnsupported is returned when environment variables or a .env
	// are requested for a target that is not a POSIX shell.
	ErrEnvUnsupported = errors.New("host does not support environment variables")
	// ErrEnvTooLarge is returned when the quoted command line carrying the
	// environment would exceed envfile.MaxExportBytes.
	ErrEnvTooLarge = errors.New("environment is too large for the host command line")
)

// StartOptions parameterises StartWithEnv.
type StartOptions struct {
	Dir        string // absolute remote start directory, "" for the sshd default
	Rows, Cols uint16
	Env        []envfile.Entry // the session's own entries (Default flags honoured by Merge)
	LoadDotenv bool            // read ".env" from Dir, or the SFTP start directory when Dir == ""
}

const (
	dotenvName           = ".env"
	warnDotenvMissing    = "no .env file found"
	warnDotenvUnreadable = "cannot read .env"
	warnDotenvTooLarge   = ".env too large, skipped"
)

// readRemoteDotenv reads and parses the one fixed file name ".env" under dir
// (or the SFTP start directory, the account's home on a stock sshd, when dir
// is empty) over the already-open SFTP client. There is no caller-supplied
// path: dir is the session's own start directory, bounded by the ownership
// check on the session like every SSH path. Every failure is a warning —
// the session still starts — and warnings never contain file content.
func readRemoteDotenv(sc *sftp.Client, dir string) ([]envfile.Entry, []string) {
	base := dir
	if base == "" {
		var err error
		if base, err = sc.Getwd(); err != nil {
			return nil, []string{warnDotenvUnreadable}
		}
	}
	p := path.Join(base, dotenvName)

	fi, err := sc.Stat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, []string{warnDotenvMissing}
		}
		return nil, []string{warnDotenvUnreadable}
	}
	if !fi.Mode().IsRegular() {
		return nil, []string{warnDotenvUnreadable}
	}
	f, err := sc.Open(p)
	if err != nil {
		return nil, []string{warnDotenvUnreadable}
	}
	defer f.Close()
	// One byte over the cap tells "exactly at the cap" from "larger".
	data, err := io.ReadAll(io.LimitReader(f, envfile.MaxFileBytes+1))
	if err != nil {
		return nil, []string{warnDotenvUnreadable}
	}
	if len(data) > envfile.MaxFileBytes {
		return nil, []string{warnDotenvTooLarge}
	}
	return envfile.Parse(data)
}
