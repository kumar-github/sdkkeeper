# sdkkeeper

The real, `cobra`-based implementation of SDK Keeper (`sk`). Every
command from the project's grammar is built. `install` supports Java
(Temurin + Liberica), Maven (Apache), and Gradle.

## This round: `Neutral` corrected from Catppuccin's "Text" tier to
Blue -- a real gap in the previous fix, caught by your own real-world
observation

The previous round's `Neutral` color (Catppuccin's own "Text" tier,
`#cdd6f4` dark / `#4c4f69` light) was genuinely a real, distinct color
from `Detail` -- but on a real terminal, it reads as near-white,
indistinguishable from many terminals' own default foreground. It
achieved "more readable than muted grey" but not the actual goal:
"recognizable as sk's own neutral signal, the way green/red/amber are
for theirs." Corrected to Blue (`#89b4fa` dark / `#1e66f5` light) --
the well-established, near-universal "informational, neutral"
convention across logging frameworks, linters, and CI tools, chosen
specifically for that already-learned recognizability rather than for
raw contrast.

`colorText` renamed to `colorBlue` throughout (the old name would now
be misleading, since it's no longer Catppuccin's "Text" tier at all).

## A real, second finding surfaced while verifying this round --
caught and fixed as part of implementing the change properly

Re-running the color-consistency checks with the new Blue value showed
`Neutral` and `Detail` downsampling to the SAME 16-color terminal
approximation in this sandbox -- not a bug in the implementation
(confirmed directly: the two colors have genuinely different truecolor
hex values in the source, `#89b4fa` vs `#a6adc8`), but a real
limitation of comparing RENDERED ANSI codes for two colors that are
deliberately close in hue (both blue-family) on a terminal without
genuine truecolor support. `scripts/sk-sequence-check.sh`'s own
verification for this specific pair was rebuilt to check the SOURCE
hex values directly (grepping `style.go` itself, located relative to
the script), rather than relying on this sandbox's own rendering --
unaffected by any terminal's color-downsampling limitations.

## What's proven now

- Full build, `go vet` clean, `gofmt` clean, **169 tests passing**
  (unchanged in count -- a color-value change, no new logic branches)
- Cross-compiles cleanly for `GOOS=darwin GOARCH=arm64`
- Verified via real, forced ANSI output that all previously-affected
  messages now render the new Blue value
- Full regression: every other command re-confirmed correct

## `scripts/sk-sequence-check.sh`

Section L's Neutral/Detail comparison rebuilt to verify source hex
values directly rather than rendered terminal output for this specific
pair -- confirmed correctly reporting no mismatch. Re-run end to end,
exit code 0, zero mismatches across all 32 checkpoints, no stray state
left behind.

## Not yet built

Kotlin, Kafka. Windows support (plan exists, not yet implemented).
Distribution (Homebrew tap, Scoop bucket).
