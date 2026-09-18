package tasks

import (
	"github.com/Andste82/sessile/backend/internal/agents"
)

// requestInstruction is the one first message an agent gets when the task has
// a request: the request itself stays in PROMPT.md and never reaches a
// command line (§4.12.4).
const requestInstruction = "Read PROMPT.md: that's my request."

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
	def    agentDef
	first  []string // argv for the first start
	resume []string // argv to continue the conversation
	env    [][2]string
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

	first := append([]string{def.Binary}, common...)
	if hasRequest {
		if def.RequestFlag != "" {
			first = append(first, def.RequestFlag)
		}
		first = append(first, requestInstruction)
	}
	resume := append([]string{def.Binary}, def.ResumeArgs...)
	resume = append(resume, common...)
	return launch{def: def, first: first, resume: resume, env: env}, true
}
