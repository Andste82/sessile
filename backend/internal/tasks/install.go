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

// installFor returns the user-space installer the bootstrap runs when an
// agent's CLI is missing (§4.12.5), "" when there is none. inContainer is
// true when it runs inside a devcontainer.
func installFor(binary string, inContainer bool) string {
	return ""
}

// installForPS is installFor for task.ps1.
func installForPS(binary string) string {
	return ""
}

func osMode(perm uint32) os.FileMode { return os.FileMode(perm) }
