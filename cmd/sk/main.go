// Command sk is SDK Keeper's binary entry point. Deliberately thin --
// all real logic lives in internal/cli and the packages it wires
// together; this file only exists to hand off to cobra and translate
// the result into a process exit code.
package main

import (
	"os"

	"sdkkeeper/internal/cli"
)

// version is injected at build time via -ldflags "-X main.version=1.2.3",
// sourced from a real git tag by an eventual release process -- never
// hardcoded here. The linker target is literally "main.version", not
// this package's module-relative path (sdkkeeper/cmd/sk) -- confirmed
// directly: Go's linker refers to whichever package is being compiled
// as the entry point simply as "main", regardless of where it actually
// lives in the module, since only one main package is ever linked into
// a given binary. Defaults to "dev" for anyone building locally from
// source without that pipeline (this project has no release process
// yet at all -- no git tags, no GoReleaser -- so "dev" is the honest,
// correct value until one exists), matching how kubectl/docker/
// terraform all identify an unreleased, locally-built binary.
var version = "dev"

// commit is injected at build time via -ldflags "-X main.commit=<sha>",
// alongside version -- see version's own doc comment. Empty for a
// local "dev" build, same as version has no real git-tag meaning
// then either.
var commit = ""

func main() {
	err := cli.Execute(version, commit)
	// nil -> 0, a *cli.CLIError -> its own exit code (--format=json's
	// error-code table), anything else -> 1, unchanged from before
	// that table existed.
	os.Exit(cli.ExitCode(err))
}
