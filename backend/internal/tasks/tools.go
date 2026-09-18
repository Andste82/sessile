package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/Andste82/sessile/backend/internal/agents"
)

// ToolServer is the sessile MCP server (internal/mcp, §4.17.3) as tasks
// need it: to serve a task's tunnel, to render the instructions' Tools
// section, and to hand out the bridge binary.
type ToolServer interface {
	ToolsSection(userID string) string
	Serve(l net.Listener, userID, taskID, token string)
	Bridge(goos, goarch string) ([]byte, bool)
}

// toolsSetup is how one start of a task reaches its tools.
type toolsSetup struct {
	enabled bool
	// agentDir and bridge are paths as the agent sees them: the task folder
	// and the bridge in it (/sessile/task/... inside a devcontainer).
	agentDir, bridge string
	windows          bool
	// files to write into the task folder: token, port, bridge binaries.
	files []file
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mcpFiles are the agent's MCP registration, written into the task folder
// (§4.17.2): one file per CLI, all naming the bridge and nothing else.
func mcpFiles(a agents.Agent, t toolsSetup) []file {
	if !t.enabled {
		return nil
	}
	server := map[string]any{"type": "stdio", "command": t.bridge, "args": []string{}}
	switch a {
	case agents.AgentClaude:
		cfg, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"sessile": server}}, "", "  ")
		settings, _ := json.MarshalIndent(map[string]any{
			"permissions": map[string]any{"allow": []string{"mcp__sessile"}},
		}, "", "  ")
		return []file{{".sessile-mcp.json", append(cfg, '\n'), 0o600}, {".sessile-claude-settings.json", append(settings, '\n'), 0o600}}
	case agents.AgentGemini:
		gem := map[string]any{"command": t.bridge, "args": []string{}, "trust": true}
		cfg, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"sessile": gem}}, "", "  ")
		return []file{{".gemini/settings.json", append(cfg, '\n'), 0o600}}
	}
	return nil
}

// goArch maps `uname -m` to Go's architecture names; "" for one sessile has
// no bridge for.
func goArch(machine string) string {
	switch strings.TrimSpace(strings.ToLower(machine)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return ""
}

// remoteArch asks the target what it runs on: a fixed command, the same kind
// of internal typed read as hostops' process listing (§4.10).
func remoteArch(client *ssh.Client, windows bool) (string, error) {
	s, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	cmd := "uname -m"
	if windows {
		cmd = `powershell -NoProfile -NonInteractive -Command "$env:PROCESSOR_ARCHITECTURE"`
	}
	out, err := s.Output(cmd)
	if err != nil {
		return "", err
	}
	return goArch(string(out)), nil
}

// openSSHTunnel opens the task's tunnel on the session's own connection
// (§4.17.3): a Unix socket in the task folder where the target's sshd allows
// stream-local forwarding (a devcontainer then sees it through the task-folder
// mount), otherwise — and always on Windows — a loopback TCP port. It returns
// the listener and the files the bridge finds it by.
func openSSHTunnel(client *ssh.Client, sc *sftp.Client, dir string, windows bool) (net.Listener, []file, error) {
	sock := path.Join(dir, ".sessile.sock")
	_ = sc.Remove(sock) // sshd won't bind over a stale socket
	_ = sc.Remove(path.Join(dir, ".sessile-port"))
	if !windows {
		if l, err := client.ListenUnix(sock); err == nil {
			return l, nil, nil
		}
	}
	l, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("open the tools tunnel: %w", err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		l.Close()
		return nil, nil, errors.New("the tools tunnel has no port")
	}
	return l, []file{{".sessile-port", []byte(fmt.Sprintf("%d\n", addr.Port)), 0o600}}, nil
}

// bridgeFiles picks the bridge binaries a start needs: one for where the
// agent runs (the host, or inside a Linux devcontainer), under .tools/.
func bridgeFiles(ts ToolServer, goos, goarch string, windows bool) (hostName string, files []file, ok bool) {
	b, ok := ts.Bridge(goos, goarch)
	if !ok {
		return "", nil, false
	}
	name := "sessile-mcp"
	if windows {
		name += ".exe"
	}
	return name, []file{{".tools/" + name, b, 0o700}}, true
}

// localListener is a local-host task's tunnel: a Unix socket in its folder
// on the server itself. The previous start's listener is closed first.
func (s *Service) localListener(taskID, dir string) (net.Listener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.local == nil {
		s.local = map[string]net.Listener{}
	}
	if old := s.local[taskID]; old != nil {
		old.Close()
	}
	sock := dir + "/.sessile.sock"
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(sock, 0o600)
	s.local[taskID] = l
	return l, nil
}

// sshTools sets up one SSH start's tools (§4.17.3): the bridge for wherever
// the agent runs, the tunnel on the session's connection, and the token. Any
// failure leaves the task without tools rather than failing its start; the
// listener is nil then.
func (s *Service) sshTools(client *ssh.Client, sc *sftp.Client, dir string, windows, inContainer bool) (toolsSetup, net.Listener, string) {
	none := toolsSetup{}
	if s.Tools == nil {
		return none, nil, ""
	}
	arch, err := remoteArch(client, windows)
	if err != nil || arch == "" {
		s.warn("tools unavailable: unknown host architecture", err)
		return none, nil, ""
	}
	// The agent runs in a Linux container, or on the host itself. macOS hosts
	// have no bridge build.
	goos := "linux"
	if windows && !inContainer {
		goos = "windows"
	}
	name, bins, ok := bridgeFiles(s.Tools, goos, arch, goos == "windows")
	if !ok {
		s.warn("tools unavailable: no bridge for "+goos+"/"+arch, nil)
		return none, nil, ""
	}
	l, portFiles, err := openSSHTunnel(client, sc, dir, windows)
	if err != nil {
		s.warn("tools unavailable", err)
		return none, nil, ""
	}
	token := newToken()
	t := toolsSetup{enabled: true, windows: goos == "windows", files: append(bins, portFiles...)}
	t.files = append(t.files, file{".sessile-token", []byte(token + "\n"), 0o600})
	switch {
	case inContainer:
		t.agentDir, t.bridge = "/sessile/task", "/sessile/task/.tools/"+name
	case windows:
		t.agentDir = windowsPath(dir)
		t.bridge = t.agentDir + `\.tools\` + name
	default:
		t.agentDir, t.bridge = dir, dir+"/.tools/"+name
	}
	return t, l, token
}

// localTools is sshTools for a local-host task: the bridge for the server's
// own platform and a Unix socket in the task folder.
func (s *Service) localTools(userID, taskID, dir string, inContainer bool) toolsSetup {
	if s.Tools == nil {
		return toolsSetup{}
	}
	name, bins, ok := bridgeFiles(s.Tools, "linux", runtimeArch(), false)
	if !ok {
		s.warn("tools unavailable: no bridge for this server", nil)
		return toolsSetup{}
	}
	l, err := s.localListener(taskID, dir)
	if err != nil {
		s.warn("tools unavailable", err)
		return toolsSetup{}
	}
	token := newToken()
	go s.Tools.Serve(l, userID, taskID, token)
	t := toolsSetup{enabled: true, files: append(bins, file{".sessile-token", []byte(token + "\n"), 0o600})}
	if inContainer {
		t.agentDir, t.bridge = "/sessile/task", "/sessile/task/.tools/"+name
	} else {
		t.agentDir, t.bridge = dir, dir+"/.tools/"+name
	}
	return t
}

func (s *Service) warn(msg string, err error) {
	if s.Log != nil {
		s.Log.Warn(msg, "err", err)
	}
}
