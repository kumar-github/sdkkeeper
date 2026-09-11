package cli

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"sdkkeeper/internal/picker"
	"sdkkeeper/internal/registry"
	"sdkkeeper/internal/tooldef"
)

// parseFullIdentifier checks whether arg is a COMPLETE version+vendor
// identifier for toolName (e.g. "21.0.2-temurin") -- both the exact
// patch and the vendor present and unambiguous, needing no picker at
// all.
//
// A single-vendor tool (see hasSingleVendor) also accepts a BARE
// version with no suffix at all as a complete identifier -- with only
// one possible source, there's no real ambiguity a suffix would need
// to resolve, so requiring e.g. "3.9.14-apache" on every Maven version
// would be pure, unnecessary friction. An explicit suffix still also
// works even for a single-vendor tool, for forward-compatibility if
// that tool ever gains a second vendor later.
//
// For a multi-vendor tool (java), deliberately strict: a partial
// identifier that only supplies ONE piece -- e.g. "21-temurin" (names
// a vendor but not an exact patch), or "21.0.2" (names a patch but not
// a vendor) -- does NOT count as complete here. This is a deliberate
// design decision, not an oversight: rather than trying to "smartly"
// skip only whichever picker stages are already implied by partial
// input (a real combinatorial matrix -- 3 independent unknowns means
// up to 8 different flows, each its own code path to reason about and
// maintain), anything less than a FULL identifier always goes through
// the exact same fixed vendor -> major -> patch picker sequence, no
// exceptions. A little convenience is traded away (typing "21-temurin"
// doesn't skip the vendor picker) for a single flow that's simple to
// predict as a user and simple to maintain as code.
func parseFullIdentifier(toolName, arg string) (version, vendor string, ok bool) {
	if arg == "" {
		return "", "", false
	}
	names := vendorNamesFor(toolName)

	for _, name := range names {
		suffix := "-" + name
		if !strings.HasSuffix(arg, suffix) {
			continue
		}
		versionPart := strings.TrimSuffix(arg, suffix)
		// Must ALSO look like a full patch version (contains a "."),
		// not just a bare major -- "21-temurin" names a vendor but not
		// an exact patch, so it's still incomplete.
		if versionPart != "" && strings.Contains(versionPart, ".") {
			return versionPart, name, true
		}
	}

	if len(names) == 1 && strings.Contains(arg, ".") {
		return arg, names[0], true
	}
	return "", "", false
}

// resolveVendorAndVersion runs the fixed vendor -> major -> patch
// picker sequence, always in this order, always all three stages --
// see parseFullIdentifier's doc comment for why this never tries to
// skip stages based on partial input. Vendor is asked FIRST, always,
// not last: major/patch version lists are fetched from that specific
// vendor's own API, so which catalog to even query is only known once
// the vendor is chosen -- a real data dependency, not just a stylistic
// ordering choice.
//
// For a single-vendor tool (see hasSingleVendor), the vendor stage is
// skipped entirely -- there's no real choice to present, so showing a
// picker with exactly one option would be friction with no purpose,
// not consistency.
func resolveVendorAndVersion(ctx context.Context, tool tooldef.Tool) (registry.Provider, string, error) {
	names := vendorNamesFor(tool.Name)

	var provider registry.Provider
	if len(names) == 1 {
		provider = providersFor(tool.Name)[names[0]]
	} else {
		// The picker shows CAPITALIZED display names (e.g. "Temurin",
		// not "temurin") -- but its return value must map back to
		// the original, lowercase name before use: that's the real
		// map key providersFor is keyed by, and also the exact
		// string embedded in version identifiers (e.g.
		// "21.0.2-temurin"), which must stay lowercase there
		// regardless of how it's displayed here.
		displayNames := make([]string, len(names))
		for i, name := range names {
			displayNames[i] = capitalize(name)
		}
		chosenDisplay, err := picker.Run(session, fmt.Sprintf("Select %s vendor", tool.DisplayName), displayNames, "")
		if err != nil {
			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Error.Render(err.Error()))
			return nil, "", err
		}
		if chosenDisplay == "" {
			fmt.Fprintln(session.Out)
			// Styled Detail (neutral), not Error -- a picker being
			// cancelled is the user's own deliberate choice, not a
			// failure; see Styles' own doc comment for the full
			// convention.
			fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("No %s vendor selected to install.", tool.DisplayName)))
			return nil, "", fmt.Errorf("no vendor selected")
		}
		var chosenVendor string
		for i, d := range displayNames {
			if d == chosenDisplay {
				chosenVendor = names[i]
				break
			}
		}
		provider = providersFor(tool.Name)[chosenVendor]
	}

	majors, err := provider.ListMajorVersions(ctx)
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(
			fmt.Sprintf("\u2717 Could not list available %s major versions: %s", tool.DisplayName, err),
		))
		return nil, "", err
	}
	chosenMajor, err := picker.Run(session, fmt.Sprintf("Select %s major version", tool.DisplayName), majors, "")
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(err.Error()))
		return nil, "", err
	}
	if chosenMajor == "" {
		fmt.Fprintln(session.Out)
		// Styled Detail (neutral), not Error -- see the vendor-picker
		// case above for the full reasoning.
		fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("No %s major version selected to install.", tool.DisplayName)))
		return nil, "", fmt.Errorf("no major version selected")
	}

	patches, err := provider.ListPatchVersions(ctx, chosenMajor, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		if err == registry.ErrVersionNotFound {
			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Error.Render(
				fmt.Sprintf("\u2717 No %s %s releases found for %s/%s via %s", tool.DisplayName, chosenMajor, runtime.GOOS, runtime.GOARCH, provider.Name()),
			))
			return nil, "", fmt.Errorf("no releases found")
		}
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(
			fmt.Sprintf("\u2717 Could not list %s %s versions: %s", tool.DisplayName, chosenMajor, err),
		))
		return nil, "", err
	}
	chosenPatch, err := picker.Run(session, fmt.Sprintf("Select %s %s minor/patch version", tool.DisplayName, chosenMajor), patches, "")
	if err != nil {
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(err.Error()))
		return nil, "", err
	}
	if chosenPatch == "" {
		fmt.Fprintln(session.Out)
		// Styled Detail (neutral), not Error -- see the vendor-picker
		// case above for the full reasoning.
		fmt.Fprintln(session.Out, styles.Neutral.Render(fmt.Sprintf("No %s minor/patch version selected to install.", tool.DisplayName)))
		return nil, "", fmt.Errorf("no version selected")
	}

	return provider, chosenPatch, nil
}

// alreadyExistsMessage builds the "already installed" error message
// for a target that already exists at targetDir -- distinguishing a
// real, sk-installed directory from a symlink (registered via `sk
// add`, pointing elsewhere) so the message doesn't misleadingly claim
// something was "installed" when it was actually just added/pointed
// to. Confirmed via real use: a JDK added via `sk add` (a symlink)
// was previously reported as "installed" at the .sdkkeeper/candidates
// path -- technically wrong, since sk never installed anything there.
//
// For a symlink, shows the REAL, resolved location where the actual
// files live (not the redirect path itself, which isn't where a user
// would think to look), with "(not managed by SDK Keeper)" as a
// trailing qualifier -- reusing the exact phrase already established
// in `list`'s own output, so it's immediately recognizable rather
// than new wording to parse. Falls back to showing the symlink's own
// path if resolving the real target fails (rare: permissions, a
// genuinely broken/dangling link) -- better to show SOMETHING than
// let the message fail to render at all.
