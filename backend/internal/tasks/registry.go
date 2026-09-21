package tasks

import (
	"github.com/Andste82/sessile/backend/internal/agents"
)

// requestInstruction is the one first message an agent gets when the task has
// a request: the request itself stays in PROMPT.md and never reaches a
// command line (§4.12.4).
// It names the machine, because every other file the agent touches is on the
// task's host and reaching for this one there finds nothing (v0.9).
const requestInstruction = "Read ./PROMPT.md with your own Read tool — it is here, on this machine, beside you — that's my request."

// agentDef is one built-in agent (§4.12.4). Every argv here is constant; the
// only variable part any of them takes is a model id that passed
// agents.ValidModel.
type agentDef struct {
	// Binary is the command name the bootstrap looks for.
	Binary string
	// InstructionsFile is the file the agent reads from its working
	// directory on its own.
	InstructionsFile string
	// PlanArgs start the agent in its plan (or closest read-only) mode.
	PlanArgs []string
	// RequestFlag precedes the first message, "" when it is positional.
	RequestFlag string
	// ResumeArgs continue the latest conversation in the working directory.
	ResumeArgs []string
	// ModelEnv carries the model when the CLI reads one from env; otherwise
	// ModelFlag is passed with it.
	ModelEnv  string
	ModelFlag string
	// Env is exported on every start.
	Env map[string]string
}

// registry was checked against each CLI's docs and source on 2026-09-18
// (Claude Code docs; openai/codex @7498521; google-gemini/gemini-cli 0.62).
var registry = map[agents.Agent]agentDef{
	agents.AgentClaude: {
		Binary:           "claude",
		InstructionsFile: "CLAUDE.md",
		PlanArgs:         []string{"--permission-mode", "plan"},
		ResumeArgs:       []string{"--continue"},
		ModelEnv:         "ANTHROPIC_MODEL",
	},
	agents.AgentCodex: {
		Binary:           "codex",
		InstructionsFile: "AGENTS.md",
		// codex has no plan flag (/plan is TUI-only); a read-only sandbox that
		// asks before acting is the closest start.
		PlanArgs:   []string{"--sandbox", "read-only", "--ask-for-approval", "on-request"},
		ResumeArgs: []string{"resume", "--last"},
		ModelFlag:  "-m",
	},
	agents.AgentGemini: {
		Binary:           "gemini",
		InstructionsFile: "GEMINI.md",
		PlanArgs:         []string{"--approval-mode", "plan"},
		RequestFlag:      "-i",
		ResumeArgs:       []string{"--resume", "latest"},
		ModelEnv:         "GEMINI_MODEL",
		// A fresh task folder is an untrusted workspace, and gemini exits in
		// one rather than asking.
		Env: map[string]string{"GEMINI_CLI_TRUST_WORKSPACE": "true"},
	},
}

// codexAPIKeyArgs make the interactive codex read an API key from
// OPENAI_API_KEY: its built-in provider only uses a login, and the TUI
// doesn't pick the variable up on its own (§4.13).
var codexAPIKeyArgs = []string{
	"-c", `model_provider="sessile_openai"`,
	"-c", `model_providers.sessile_openai.name="OpenAI"`,
	"-c", `model_providers.sessile_openai.base_url="https://api.openai.com/v1"`,
	"-c", `model_providers.sessile_openai.env_key="OPENAI_API_KEY"`,
	"-c", `model_providers.sessile_openai.wire_api="responses"`,
}

// launch is the resolved start of one task's agent.
type launch struct {
	agent      agents.Agent
	def        agentDef
	common     []string // mode, model and connection arguments
	hasRequest bool
	env        [][2]string
	first      []string // argv for the first start, without tools
	resume     []string // argv to continue the conversation, without tools
}

// start is the argv for this start: continuing the conversation where the
// agent has run before, a first start otherwise (§4.12.6).
func (l launch) start(resumed bool) []string {
	if resumed {
		return l.resume
	}
	return l.first
}

// resolveLaunch builds the argv for an agent, mode, model and connection.
func resolveLaunch(a agents.Agent, connKind, mode, model string, hasRequest bool) (launch, bool) {
	def, ok := registry[a]
	if !ok {
		return launch{}, false
	}
	common := []string{}
	if mode == ModePlan {
		common = append(common, def.PlanArgs...)
	}
	var env [][2]string
	if model != "" {
		if def.ModelEnv != "" {
			env = append(env, [2]string{def.ModelEnv, model})
		} else if def.ModelFlag != "" {
			common = append(common, def.ModelFlag, model)
		}
	}
	if a == agents.AgentCodex && connKind == "codex-api" {
		common = append(common, codexAPIKeyArgs...)
	}
	for k, v := range def.Env {
		env = append(env, [2]string{k, v})
	}
	ln := launch{agent: a, def: def, common: common, hasRequest: hasRequest, env: env}
	ln.first, ln.resume = ln.argv(nil)
	return ln, true
}

// argv builds both start commands, with the agent's MCP arguments (§4.17.2)
// when the task has tools. The tool arguments go first: Claude Code's
// --mcp-config takes several values and would swallow a positional prompt
// after it.
func (ln launch) argv(toolArgs []string) (first, resume []string) {
	first = append([]string{ln.def.Binary}, toolArgs...)
	first = append(first, ln.common...)
	if ln.hasRequest {
		if ln.def.RequestFlag != "" {
			first = append(first, ln.def.RequestFlag)
		}
		first = append(first, requestInstruction)
	}
	resume = append([]string{ln.def.Binary}, ln.def.ResumeArgs...)
	resume = append(resume, toolArgs...)
	resume = append(resume, ln.common...)
	return first, resume
}

// toolArgs are the agent's command-line part of registering the sessile MCP
// server; mcpConfig and claudeSettings are files in the task folder, written
// alongside (see mcpFiles).
func toolArgs(a agents.Agent, dir, bridge string, windows bool) []string {
	join := pathJoiner(windows)
	switch a {
	case agents.AgentClaude:
		// --mcp-config loads the server for this run only, next to the
		// user's own; --settings pre-allows its tools in Claude Code (the
		// approval that counts for writes is sessile's own, §4.17.3).
		return []string{"--mcp-config", join(dir, ".sessile-mcp.json"), "--settings", join(dir, ".sessile-claude-settings.json")}
	case agents.AgentCodex:
		// TOML literal strings: no escapes, so a Windows path stays as is.
		return []string{"-c", "mcp_servers.sessile.command='" + bridge + "'", "-c", "mcp_servers.sessile.args=[]"}
	}
	// gemini reads .gemini/settings.json from its working directory.
	return nil
}

func pathJoiner(windows bool) func(dir, name string) string {
	if windows {
		return func(dir, name string) string { return dir + `\` + name }
	}
	return func(dir, name string) string { return dir + "/" + name }
}
