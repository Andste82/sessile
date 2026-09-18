package tasks

import "os"

// installFor returns the user-space installer the bootstrap runs when an
// agent's CLI is missing (§4.12.5), "" when there is none yet.
func installFor(binary string) string {
	return ""
}

// installForPS is installFor for task.ps1.
func installForPS(binary string) string {
	return ""
}

func osMode(perm uint32) os.FileMode { return os.FileMode(perm) }
