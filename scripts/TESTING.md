# Testing sk: sequences over combinations

`scripts/sk-sequence-check.sh` is a real, runnable regression guide.
This file explains the reasoning behind it, so it can keep growing
as new bug classes get found — not just stay a fixed snapshot of
today's known issues.

## Why not test every combination?

It's not feasible. `sk` has enough dimensions (tool × version state ×
vendor count × session history × which shell) that a genuinely
exhaustive matrix would be enormous, and most of it wouldn't be
meaningfully different from a case already covered. The real question
isn't "did we test everything" — it's "did we test the *shapes* of
interaction that actually cause bugs in a tool like this."

## What the real bugs in this project actually had in common

Looking back at the real, reported bugs so far, they cluster into a
few repeating patterns, not a random scatter:

1. **State accumulated across a SEQUENCE of commands, not any single
   command.** The `PATH`-accumulation bug only existed because of
   `use v1` → `use v2` → `remove v2`, in that order, in one shell
   session. Testing `remove v2` alone, freshly, never exposed it.
2. **A divergence between what `sk` reports and what's independently,
   actually true.** `sk current java` saying "nothing active" while
   `java -version` disagreed. The bug was never visible from `sk`'s
   own output alone — only by checking both.
3. **A code path gated by something the testing environment doesn't
   have.** The Liberica `Contents/Home` bug is gated by
   `runtime.GOOS == "darwin"` — no amount of testing on a Linux
   sandbox could ever reach it.
4. **An edge of the input space that wasn't part of the "obvious"
   happy path.** No version given vs. an explicit one; a tool with
   zero installed versions vs. one; the specific version requested
   existing vs. not.

The script is organized around reproducing these four shapes
directly, not around "here's every flag of every command."

## The two things every section in the script does

**Runs a realistic sequence**, in the order a real session would
actually produce it — not each command reset to a pristine state.

**Prints `sk`'s own report next to independently-verified ground
truth** — `$JAVA_HOME`, `command -v java`, and the fake JDK's own
identifying output, side by side with whatever `sk current`/`sk list`
says. A real bug in this pattern shows up as a *divergence* between
these, not as one obviously wrong number.

## What this script honestly cannot catch

- **Anything gated by `runtime.GOOS == "darwin"`.** This needs to run
  on an actual Mac to mean anything for that code path. The script is
  zsh, so it *can* run there directly — but darwin-specific logic
  inside `sk` itself still only executes when the binary was actually
  built for and run on darwin.
- **The interactive picker's actual rendering.** Section D notes
  where a picker should appear; the script can't drive real terminal
  interaction, so that part still needs a human watching.
- **Real network calls.** Nothing here hits `services.gradle.org`,
  `archive.apache.org`, or any vendor API — those need the sandbox's
  or your machine's actual network access, exercised separately.
- **Nushell.** The script itself is zsh (matching the shell the
  bug reports have come from). The *underlying* Go logic it exercises
  is shell-agnostic, but the wrapper-function mechanics (`hide-env`,
  `load-env`, JSON parsing) are genuinely different code paths in
  `internal/shellhook/templates/init.nu.tmpl` and need their own,
  separate real Nushell run to verify — the same way this project's
  own development process has done it each time shell-integration
  code changed.

## A real, genuine gap this sandbox has -- and a real fix for part of it

`go test` in this sandbox never has a real controlling terminal at
all, so `term.Open()` always falls back to `os.Stderr`, silently
masking any code that behaves differently when a real `/dev/tty` IS
available. This is not hypothetical: a real bug shipped and passed
every test in this sandbox, then failed immediately on a real
machine, with exactly this cause -- `cli.requireArgs`'s error message
was written via a fresh `term.Open()` call, which correctly opens
`/dev/tty` and writes there whenever a real terminal exists. On a real
machine, that meant the message bypassed `os.Stderr` entirely (still
genuinely visible to the user, but invisible to a test using a
standard `os.Stderr` redirect) -- the test had only ever been passing
in this sandbox by coincidence, not because the code was correct.

**`script -qc "<command>" /dev/null` gives a process a real pseudo-TTY
in this sandbox**, closing part of this gap for Go's own test suite
specifically (this is unrelated to `sk-sequence-check.sh` itself,
which is already real zsh regardless). Confirmed directly, both
directions: the broken version of the fix above genuinely failed
under `script`, with the EXACT error message the real bug report
showed (`expected the usage message to include cmd.Use, got: ""`) --
and the actual fix genuinely passed under the same real-PTY
conditions. Use this whenever a change touches `term.Open()`,
`session.Out`, or anything that behaves differently depending on
`HasTTY`:

```bash
script -qc "go test ./... -v -count=1" /dev/null
```

`-count=1` disables Go's test cache -- without it, a result from an
earlier, real (non-PTY) run can silently mask what actually happens
under the PTY this time. This still doesn't cover everything a real
machine does (the picker's actual rendering still needs a human
watching, as below) -- but it's a real, meaningful step closer for
anything gated purely on "is there a controlling terminal at all",
without needing a real machine for every single check of that kind.

## Cross-compilation checks -- compile-only, but still real signal

This sandbox can only ever confirm this project's code COMPILES for
darwin and windows (`GOOS=darwin GOARCH=arm64 go build ./...`,
`GOOS=windows GOARCH=amd64 go build ./...` and the `go vet` equivalent
of each) -- it cannot execute either binary, so this is genuinely
weaker evidence than the actual `sk-sequence-check.sh` runs above,
which exercise real behavior on a real (Linux) binary. Still run
BOTH, every round, alongside the Linux build: a change that only
compiles on Linux (an unguarded `/dev/tty` reference outside
`tty_unix.go`, a Unix-only import with no build tag) is exactly the
kind of mistake compile-only checking on the OTHER two platforms
catches immediately, cheaply, before it ever reaches a real Windows or
macOS machine at all.

```bash
GOPROXY=direct GOSUMDB=off GOOS=darwin  GOARCH=arm64 go build ./... && go vet ./...
GOPROXY=direct GOSUMDB=off GOOS=windows GOARCH=amd64 go build ./... && go vet ./...
```

Genuine Windows behavior -- `CONIN$`/`CONOUT$` actually opening a real
console the way documented, `Invoke-Expression` actually activating a
version in a real PowerShell session, PowerShell's execution policy
actually blocking (or not) `$PROFILE` -- remains unverified from this
sandbox, the same honest, stated limitation as every other
real-machine-only item in this project. Flagged as first-priority
real-machine testing, not silently assumed correct just because it
compiles.

## When to run it

After any change that touches: `use`, `remove`, `default`, `list`,
`current`, the shell wrapper templates, or `tooldef`'s path-resolution
logic (`HomePath`/`BinPath`). Not needed for changes scoped to a
single vendor provider (e.g. adding Kotlin) unless that change also
touches shared resolution code.

## How to extend it when a new bug is found

Add a new lettered section, following the existing shape:
1. Set up the exact state that exposed the bug (fake JDKs, prior
   `use`/`remove` calls — whatever sequence actually triggered it).
2. Run the command that failed.
3. Add a `→ EXPECT:` line stating what should happen and why — not
   just "correct output", but the actual reasoning, the way the
   existing sections do. That's what makes this useful to someone
   (including a future you) who didn't live through the original bug.

The goal is that this file's sections become, over time, a working
record of every real interaction shape that's ever caused a bug —
so the next change gets checked against actual project history, not
just whatever the person making the change happens to think to try.
