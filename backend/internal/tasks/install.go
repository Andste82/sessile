package tasks

import (
	"os"
)

// genericDevcontainer is sessile's own devcontainer config (§4.12.3), for a
// repo without one.
var genericDevcontainer = mustTemplateFile("templates/generic-devcontainer.json")

func mustTemplateFile(name string) []byte {
	b, err := templateFS.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}

// The user-space installers (§4.12.5): fixed commands per agent, never
// user input, needing no root or admin rights. On a host they install into
// the login user's home (~/.local), which task.sh/agent.sh already have on
// PATH. Inside a devcontainer they install into the task folder
// (/sessile/task/.tools), so the install survives a container rebuild and
// doesn't depend on who the container's user is.
//
// Checked against each vendor's install docs on 2026-09-18: Claude Code's
// native installer (claude.ai/install.sh, .ps1) installs into ~/.local/bin;
// codex and gemini are npm packages (@openai/codex, @google/gemini-cli, the
// latter needing Node 20+). npm installs use an explicit --prefix so they
// never need a writable global prefix.
var installers = map[string]struct{ host, container, windows string }{
	"claude": {
		host: `command -v curl >/dev/null 2>&1 || stay "installing claude needs curl."
	curl -fsSL https://claude.ai/install.sh | bash`,
		// The native installer writes under $HOME; pointing HOME at the task
		// folder for the install alone keeps it there.
		container: `command -v curl >/dev/null 2>&1 || stay "installing claude needs curl."
	mkdir -p "$TASK_DIR/.tools/home"
	curl -fsSL https://claude.ai/install.sh | HOME="$TASK_DIR/.tools/home" bash`,
		windows: `irm https://claude.ai/install.ps1 | iex`,
	},
	"codex": {
		host: `command -v npm >/dev/null 2>&1 || stay "installing codex needs Node.js and npm."
	npm install -g --prefix "$HOME/.local" @openai/codex`,
		container: `command -v npm >/dev/null 2>&1 || stay "installing codex needs Node.js and npm in the container."
	npm install -g --prefix "$TASK_DIR/.tools" @openai/codex`,
		windows: `irm https://chatgpt.com/codex/install.ps1 | iex`,
	},
	"gemini": {
		host: `command -v npm >/dev/null 2>&1 || stay "installing gemini needs Node.js 20+ and npm."
	npm install -g --prefix "$HOME/.local" @google/gemini-cli`,
		container: `command -v npm >/dev/null 2>&1 || stay "installing gemini needs Node.js 20+ and npm in the container."
	npm install -g --prefix "$TASK_DIR/.tools" @google/gemini-cli`,
		windows: `if (-not (Get-Command npm -ErrorAction SilentlyContinue)) { Stay 'installing gemini needs Node.js 20+ and npm.' }
	npm install -g @google/gemini-cli`,
	},
}

// installFor returns the POSIX installer for an agent; inContainer picks the
// one that installs into the task folder.
func installFor(binary string, inContainer bool) string {
	i, ok := installers[binary]
	if !ok {
		return ""
	}
	if inContainer {
		return i.container
	}
	return i.host
}

// installForPS is installFor for task.ps1.
func installForPS(binary string) string {
	return installers[binary].windows
}

func osMode(perm uint32) os.FileMode { return os.FileMode(perm) }
