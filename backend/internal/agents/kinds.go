// Package agents holds each user's agent settings (PROJECT_PLAN.md §4.13,
// §4.16): connections (the tokens and API keys the user's coding agents
// authenticate with), profiles, task defaults and Git accounts, all in one
// hand-editable <data-dir>/users/<user-id>/agent/agent.yml.
//
// Credentials are stored plaintext, like hosts.yml's — see CLAUDE.md's
// security posture and §11. TODO(security): optional encryption-at-rest,
// tracked with hosts.yml's, not built.
package agents

// Agent names a coding-agent CLI sessile knows how to start (§4.12.4).
type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
	AgentGemini Agent = "gemini"
)

// Valid reports whether a is one of the built-in agents.
func (a Agent) Valid() bool {
	switch a {
	case AgentClaude, AgentCodex, AgentGemini:
		return true
	}
	return false
}

// FieldType is how a connection field is entered and stored.
type FieldType string

const (
	FieldString FieldType = "string"
	FieldURL    FieldType = "url"
	// FieldSecret is write-only through the API: it is never sent back,
	// only whether it is set.
	FieldSecret FieldType = "secret"
)

// Field is one input of a connection kind.
type Field struct {
	Name     string    `json:"name"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required"`
	Help     string    `json:"help,omitempty"`
	// Env is the environment variable this field's value is exported as in
	// a task session. Empty for a field that only sessile itself uses.
	Env string `json:"env,omitempty"`
}

// Kind is one way of authenticating one agent (§4.13's table). The set is
// fixed in code: a new kind is a code change, never user config, because
// each one maps onto env vars a specific CLI reads.
type Kind struct {
	ID    string `json:"id"`
	Agent Agent  `json:"agent"`
	Label string `json:"label"`
	// Enterprise marks the kinds an organisation provisions (Bedrock,
	// Foundry, Vertex, ChatGPT Business) — only used to group the picker.
	Enterprise bool `json:"enterprise"`
	// Steps is what the guided dialog shows: how to get the credential.
	Steps  []string `json:"steps"`
	Fields []Field  `json:"fields"`
	// FixedEnv is exported alongside the fields' env on every task session.
	FixedEnv map[string]string `json:"fixedEnv,omitempty"`
	// Testable is true when sessile can check the credential against the
	// vendor; otherwise Test only checks that required fields are present.
	Testable bool `json:"testable"`
	// ListsModels is true when the vendor's model list can be fetched with
	// this credential; otherwise the picker offers Aliases plus free text.
	ListsModels bool `json:"listsModels"`
	// Aliases are model names the CLI itself understands, offered when the
	// list can't be fetched (or in addition to it).
	Aliases []string `json:"aliases,omitempty"`
	// ModelField names the field that pins a model on the connection
	// itself (Bedrock and Foundry need one), "" if the kind has none.
	ModelField string `json:"modelField,omitempty"`
	// DefaultExpiryDays pre-fills the expiry for credentials with a known
	// lifetime, 0 for none.
	DefaultExpiryDays int `json:"defaultExpiryDays,omitempty"`
}

var claudeAliases = []string{"sonnet", "opus", "haiku"}

// kinds is the table of §4.13, checked against each vendor's docs and
// source on 2026-09-18. Rechecked whenever a CLI changes how it reads
// credentials.
var kinds = []Kind{
	{
		ID: "claude-subscription", Agent: AgentClaude,
		Label: "Claude subscription (Pro, Max, Team, Enterprise)",
		Steps: []string{
			"On any machine with a browser (your laptop is fine), install Claude Code and run: claude setup-token",
			"Log in with the account that has the subscription when the browser opens.",
			"Copy the token it prints and paste it below. It is valid for one year.",
			"Anthropic accepts this token only from Claude Code itself, so sessile can't test it or list models with it; it is checked the first time a task uses it.",
		},
		Fields: []Field{
			{Name: "token", Label: "OAuth token", Type: FieldSecret, Required: true, Env: "CLAUDE_CODE_OAUTH_TOKEN"},
		},
		Aliases:           claudeAliases,
		DefaultExpiryDays: 365,
	},
	{
		ID: "claude-api", Agent: AgentClaude,
		Label: "Claude API key (Anthropic Console)",
		Steps: []string{
			"Open the Anthropic Console, go to API keys, and create a key.",
			"Paste it below. Usage is billed to the Console organisation, not a subscription.",
		},
		Fields: []Field{
			{Name: "apiKey", Label: "API key", Type: FieldSecret, Required: true, Env: "ANTHROPIC_API_KEY"},
		},
		Testable: true, ListsModels: true, Aliases: claudeAliases,
	},
	{
		ID: "claude-bedrock", Agent: AgentClaude, Enterprise: true,
		Label: "Claude on Amazon Bedrock",
		Steps: []string{
			"In the Amazon Bedrock console, open API keys and create a key (long-term or short-term).",
			"Make sure the Anthropic models are enabled in the region you pick.",
			"Optionally pin a model: an inference profile id such as us.anthropic.claude-sonnet-…; Test lists the ones available to the key.",
		},
		Fields: []Field{
			{Name: "region", Label: "AWS region", Type: FieldString, Required: true, Env: "AWS_REGION", Help: "e.g. eu-central-1"},
			{Name: "token", Label: "Bedrock API key", Type: FieldSecret, Required: true, Env: "AWS_BEARER_TOKEN_BEDROCK"},
			{Name: "model", Label: "Model (inference profile id)", Type: FieldString, Env: "ANTHROPIC_MODEL"},
		},
		FixedEnv:    map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"},
		Testable:    true,
		ListsModels: true,
		ModelField:  "model",
	},
	{
		ID: "claude-foundry", Agent: AgentClaude, Enterprise: true,
		Label: "Claude on Microsoft Foundry",
		Steps: []string{
			"In the Azure portal, open the Foundry resource that hosts the Claude deployment.",
			"Copy the resource name and one of its keys.",
			"Optionally pin the model deployment name.",
		},
		Fields: []Field{
			{Name: "resource", Label: "Resource name", Type: FieldString, Required: true, Env: "ANTHROPIC_FOUNDRY_RESOURCE"},
			{Name: "apiKey", Label: "API key", Type: FieldSecret, Required: true, Env: "ANTHROPIC_FOUNDRY_API_KEY"},
			{Name: "model", Label: "Model deployment", Type: FieldString, Env: "ANTHROPIC_MODEL"},
		},
		FixedEnv:   map[string]string{"CLAUDE_CODE_USE_FOUNDRY": "1"},
		Aliases:    claudeAliases,
		ModelField: "model",
	},
	{
		ID: "codex-api", Agent: AgentCodex,
		Label: "OpenAI API key",
		Steps: []string{
			"Open platform.openai.com, go to API keys, and create a key.",
			"Paste it below. Usage is billed to the API organisation.",
		},
		Fields: []Field{
			{Name: "apiKey", Label: "API key", Type: FieldSecret, Required: true, Env: "OPENAI_API_KEY"},
		},
		Testable: true, ListsModels: true,
	},
	{
		ID: "codex-chatgpt", Agent: AgentCodex, Enterprise: true,
		Label: "ChatGPT Business / Enterprise access token",
		Steps: []string{
			"A workspace owner must first allow personal access tokens for Codex.",
			"Create a personal access token for Codex in your ChatGPT workspace settings. Tokens expire (1 to 90 days); set the same expiry below.",
			"Personal ChatGPT Plus/Pro plans have no token and can't be used here; use an OpenAI API key instead.",
		},
		Fields: []Field{
			{Name: "token", Label: "Access token", Type: FieldSecret, Required: true, Env: "CODEX_ACCESS_TOKEN"},
		},
		DefaultExpiryDays: 30,
	},
	{
		ID: "gemini-api", Agent: AgentGemini,
		Label: "Gemini API key (Google AI Studio)",
		Steps: []string{
			"Open Google AI Studio and choose Get API key.",
			"Paste it below. Google-account sign-in (Google AI Pro, Code Assist via Google login) has no token and can't be used here.",
		},
		Fields: []Field{
			{Name: "apiKey", Label: "API key", Type: FieldSecret, Required: true, Env: "GEMINI_API_KEY"},
		},
		Testable: true, ListsModels: true, Aliases: []string{"auto"},
	},
	{
		ID: "gemini-vertex", Agent: AgentGemini, Enterprise: true,
		Label: "Gemini on Vertex AI",
		Steps: []string{
			"In Google Cloud, enable Vertex AI for the project and create an API key for it.",
			"Enter the project id and the location (e.g. us-central1).",
		},
		Fields: []Field{
			{Name: "project", Label: "Project id", Type: FieldString, Required: true, Env: "GOOGLE_CLOUD_PROJECT"},
			{Name: "location", Label: "Location", Type: FieldString, Required: true, Env: "GOOGLE_CLOUD_LOCATION"},
			{Name: "apiKey", Label: "API key", Type: FieldSecret, Required: true, Env: "GOOGLE_API_KEY"},
		},
		FixedEnv: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "true"},
		Aliases:  []string{"auto"},
	},
}

// Kinds returns the fixed table of connection kinds.
func Kinds() []Kind {
	out := make([]Kind, len(kinds))
	copy(out, kinds)
	return out
}

// KindByID looks a kind up by id.
func KindByID(id string) (Kind, bool) {
	for _, k := range kinds {
		if k.ID == id {
			return k, true
		}
	}
	return Kind{}, false
}

// field returns the named field of k.
func (k Kind) field(name string) (Field, bool) {
	for _, f := range k.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}
