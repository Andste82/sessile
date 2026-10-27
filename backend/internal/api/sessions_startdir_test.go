package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

func TestValidateSSHStartDir(t *testing.T) {
	linux := hosts.Host{TargetOS: hosts.OSLinux, TerminalType: "bash"}
	cases := []struct {
		name string
		host hosts.Host
		dir  string
		want string // "" = accepted
	}{
		{"empty is no start directory", linux, "", ""},
		{"absolute", linux, "/srv/app", ""},
		{"absolute with spaces and quotes", linux, "/srv/my app/it's", ""},
		{"root", linux, "/", ""},
		{"relative", linux, "srv/app", "start directory must be an absolute path"},
		{"dot", linux, ".", "start directory must be an absolute path"},
		{"tilde", linux, "~/x", "start directory must be an absolute path"},
		{"NUL", linux, "/srv/a\x00b", "start directory must be an absolute path"},
		{"newline", linux, "/srv/a\nb", "start directory must be an absolute path"},
		{"carriage return", linux, "/srv/a\rb", "start directory must be an absolute path"},
		{"too long", linux, "/" + strings.Repeat("a", maxRemotePathLen), "start directory must be an absolute path"},
		{"at the limit", linux, "/" + strings.Repeat("a", maxRemotePathLen-1), ""},
		{"windows targetOS", hosts.Host{TargetOS: hosts.OSWindows, TerminalType: "custom"}, "/c", "start directory is not supported for Windows targets"},
		{"powershell", hosts.Host{TerminalType: "powershell"}, "/c", "start directory is not supported for Windows targets"},
		{"cmd", hosts.Host{TerminalType: "cmd"}, "/c", "start directory is not supported for Windows targets"},
		{"windows host, no directory", hosts.Host{TargetOS: hosts.OSWindows}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validateSSHStartDir(c.host, c.dir); got != c.want {
				t.Errorf("validateSSHStartDir = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRespondSessionErrorMapsStartDirErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not found", &sshpty.ErrDirNotFound{Path: "/nope"}, `start directory "/nope" does not exist or is not a directory on the host`},
		{"permission denied cause", &sshpty.ErrDirNotFound{Path: "/root", Cause: errors.New("permission denied")}, "permission denied"},
		{"sftp unavailable", &sshpty.ErrSFTPUnavailable{Err: errors.New("x")}, "cannot verify start directory: the host's SFTP subsystem is unavailable"},
		{"unsupported", &sshpty.ErrDirUnsupported{}, "start directory is not supported for Windows targets"},
		{"wrapped", fmt.Errorf("spawn: %w", &sshpty.ErrDirNotFound{Path: "/w"}), `"/w"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			(&Server{}).respondSessionError(ctx, c.err)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			var body errorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != CodeValidation {
				t.Errorf("code = %q, want %q", body.Error.Code, CodeValidation)
			}
			if !strings.Contains(body.Error.Message, c.want) {
				t.Errorf("message = %q, want it to contain %q", body.Error.Message, c.want)
			}
		})
	}
}
