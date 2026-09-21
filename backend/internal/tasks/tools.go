package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/Andste82/sessile/backend/internal/agents"
)

// ToolServer is the sessile MCP server (internal/mcp, §4.12.4) as tasks need
// it: to serve one task's socket and to render the instructions' Tools
// section. Since v0.9 the agent is on this machine, so there is no tunnel and
// no bridge binary to hand out.
type ToolServer interface {
	// ToolsSection renders the instructions' Tools section for a scope
	// (§4.18.1): the task scope, or the orchestrator's.
	ToolsSection(userID, scope string) string
	Serve(l net.Listener, userID, taskID, token, scope string)
}

// toolsSetup is how one start of a task reaches its tools.
type toolsSetup struct {
	enabled bool
	// agentDir is the task folder as the agent sees it; bridge is the command
	// that speaks MCP on its stdin, with bridgeArgs. Since v0.9 that is
	// sessile's own binary, on the same machine — no upload, no arch matrix.
	agentDir, bridge string
	bridgeArgs       []string
	windows          bool
	// files to write into the task folder: token, port, bridge binaries.
	files []file
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mcpFiles are the agent's MCP registration, written into its folder
// (§4.12.4): one file per CLI, all naming the bridge command and nothing else.
func mcpFiles(a agents.Agent, t toolsSetup) []file {
	if !t.enabled {
		return nil
	}
	args := t.bridgeArgs
	if args == nil {
		args = []string{}
	}
	server := map[string]any{"type": "stdio", "command": t.bridge, "args": args}
	switch a {
	case agents.AgentClaude:
		// A task's config dir is its own and starts empty, so the CLI would
		// open its first-run wizard — a theme picker in front of the work,
		// on every task. Sessile answers it once, here.
		cfg, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"sessile": server}}, "", "  ")
		settings, _ := json.MarshalIndent(map[string]any{
			"permissions": map[string]any{"allow": []string{"mcp__sessile"}},
		}, "", "  ")
		// Two first-run questions stand between the agent and the work: the
		// theme picker, and the trust prompt for a directory it has not seen.
		// Sessile made this folder for this task, so it answers both.
		onboarded, _ := json.MarshalIndent(map[string]any{
			"hasCompletedOnboarding": true,
			"theme":                  "dark",
			"projects": map[string]any{
				t.agentDir: map[string]any{"hasTrustDialogAccepted": true},
			},
		}, "", "  ")
		return []file{
			{".sessile-mcp.json", append(cfg, '\n'), 0o600},
			{".sessile-claude-settings.json", append(settings, '\n'), 0o600},
			{agentStateDir + "/claude/.claude.json", append(onboarded, '\n'), 0o600},
		}
	case agents.AgentGemini:
		gem := map[string]any{"command": t.bridge, "args": args, "trust": true}
		cfg, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"sessile": gem}}, "", "  ")
		return []file{{".gemini/settings.json", append(cfg, '\n'), 0o600}}
	}
	return nil
}

// localListener is a local-host task's tunnel: a Unix socket in its folder on
// the server itself, or — where the socket cannot be bound, most often
// because the folder's path is longer than the ~107 bytes a Unix socket
// address holds — a loopback port, with the file the bridge finds it by. The
// previous start's listener is closed first.
func (s *Service) localListener(taskID, dir string) (net.Listener, []file, error) {
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
	_ = os.Remove(dir + "/.sessile-port")
	if l, err := net.Listen("unix", sock); err == nil {
		_ = os.Chmod(sock, 0o600)
		s.local[taskID] = l
		return l, nil, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		l.Close()
		return nil, nil, errors.New("the tools tunnel has no port")
	}
	s.local[taskID] = l
	return l, []file{{".sessile-port", []byte(fmt.Sprintf("%d\n", addr.Port)), 0o600}}, nil
}

// localTools gives the agent its tools: a Unix socket in its own folder, and
// sessile's own binary as the stdio bridge to it. The agent runs on this
// machine, so there is nothing to upload and no tunnel to open (§4.12.4).
func (s *Service) localTools(userID, taskID, dir, scope string) toolsSetup {
	if s.Tools == nil {
		return toolsSetup{}
	}
	self, err := os.Executable()
	if err != nil {
		s.warn("tools unavailable: cannot find the sessile binary", err)
		return toolsSetup{}
	}
	l, portFiles, err := s.localListener(taskID, dir)
	if err != nil {
		s.warn("tools unavailable", err)
		return toolsSetup{}
	}
	token := newToken()
	go s.Tools.Serve(l, userID, taskID, token, scope)
	return toolsSetup{
		enabled: true, agentDir: dir,
		bridge: self, bridgeArgs: []string{"mcp-bridge", dir},
		files: append(portFiles, file{".sessile-token", []byte(token + "\n"), 0o600}),
	}
}

func (s *Service) warn(msg string, err error) {
	if s.Log != nil {
		s.Log.Warn(msg, "err", err)
	}
}
