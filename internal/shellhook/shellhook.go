// Package shellhook generates the shell integration `sk init <shell>`
// prints -- the actual distribution mechanism, since a compiled
// binary can't change its parent shell's environment on its own. The
// wrapper function this package emits applies `use`'s result to a
// live shell session, via `eval "$(sk init zsh)"` (or the Nushell
// equivalent) in the user's shell config.
//
// Both templates use `command sk` / `^sk` to bypass the wrapper
// function itself and reach the real installed binary on PATH --
// without this, the wrapper would recurse into itself.
package shellhook

import (
	"embed"
	"fmt"
)

//go:embed templates/init.zsh.tmpl templates/init.nu.tmpl templates/init.ps1.tmpl
var templates embed.FS

// Get returns the shell integration script for the given shell name
// ("zsh", "nu", "powershell", or "pwsh", an alias matching PowerShell
// 7+'s own binary name). Returns an error for anything else, rather
// than a silent empty result.
func Get(shell string) (string, error) {
	var filename string
	switch shell {
	case "zsh":
		filename = "templates/init.zsh.tmpl"
	case "nu":
		filename = "templates/init.nu.tmpl"
	case "powershell", "pwsh":
		filename = "templates/init.ps1.tmpl"
	default:
		return "", fmt.Errorf("unsupported shell: %s (expected \"zsh\", \"nu\", \"powershell\", or \"pwsh\")", shell)
	}

	data, err := templates.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
