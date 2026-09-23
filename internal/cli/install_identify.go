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

// runPicker matches picker.Run's own signature exactly, and defaults
// to it -- this indirection changes NOTHING about production behavior
// (the real, interactive picker.Run is still what actually runs).
// It exists purely as a test seam: picker.Run always requires a real
// TTY-backed session, and nothing in this codebase mocks it, so
// resolveVendorAndVersion's own control flow (the major-version
// loop-back in particular) was previously unverifiable by any
// automated test -- only by manual, interactive use. Tests save the
// real value, substitute a scripted fake for the duration of one
// test, and restore it afterward.
var runPicker = picker.Run

// parseFullIdentifier checks whether arg is a complete version+vendor
// identifier for toolName (e.g. "21.0.2-temurin"), needing no picker.
// A single-vendor tool also accepts a bare version with no suffix. For
// a multi-vendor tool, deliberately strict: a partial identifier (only
// a vendor, or only a patch) doesn't count as complete -- anything
// less than the full identifier goes through the same fixed
// vendor -> major -> patch picker sequence, no partial shortcuts.
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
		// Must also look like a full patch version, not just a bare
		// major -- "21-temurin" names a vendor but not an exact patch.
		if versionPart != "" && strings.Contains(versionPart, ".") {
			return versionPart, name, true
		}
	}

	if len(names) == 1 && strings.Contains(arg, ".") {
		return arg, names[0], true
	}
	return "", "", false
}

// classifyInstallArg resolves arg into a provider+version for
// install's --format=json path, or a *jsonError distinguishing "no
// version given at all" (ErrCodeVersionRequired) from "a version was
// given but the vendor is still ambiguous for a multi-vendor tool"
// (ErrCodeVendorRequired). --format=json can show neither the
// vendor-picker nor the version-picker resolveVendorAndVersion drives
// interactively, so both incomplete-input shapes need their own
// distinct, non-interactive error instead of one catch-all.
func classifyInstallArg(tool tooldef.Tool, arg string) (registry.Provider, string, *jsonError) {
	if v, vendorName, ok := parseFullIdentifier(tool.Name, arg); ok {
		return providersFor(tool.Name)[vendorName], v, nil
	}

	if arg == "" {
		return nil, "", &jsonError{Code: ErrCodeVersionRequired, Message: fmt.Sprintf("a version is required to install %s in --format=json (the interactive picker cannot be shown)", tool.DisplayName)}
	}

	if hasSingleVendor(tool.Name) {
		// parseFullIdentifier already accepts any bare "x.y[.z]"
		// version for a single-vendor tool -- arriving here means
		// arg didn't look like a complete version at all (e.g. a
		// bare major with no dot).
		return nil, "", &jsonError{Code: ErrCodeVersionRequired, Message: fmt.Sprintf("%q is not a complete %s version -- an exact version is required in --format=json", arg, tool.DisplayName)}
	}

	names := vendorNamesFor(tool.Name)
	for _, name := range names {
		if strings.EqualFold(arg, name) {
			// A bare vendor name with no version -- the version is
			// what's missing here, not the vendor.
			return nil, "", &jsonError{Code: ErrCodeVersionRequired, Message: fmt.Sprintf("a version is required to install %s (%s) in --format=json", tool.DisplayName, name)}
		}
	}

	if strings.Contains(arg, ".") {
		// Looks like a version, but with no vendor suffix matching
		// any of this tool's known vendors -- the vendor is what's
		// missing/ambiguous here, not the version.
		return nil, "", &jsonError{Code: ErrCodeVendorRequired, Message: fmt.Sprintf("%s has multiple vendors (%s) -- specify one explicitly, e.g. %q, in --format=json", tool.DisplayName, strings.Join(names, ", "), arg+"-"+names[0])}
	}

	return nil, "", &jsonError{Code: ErrCodeVersionRequired, Message: fmt.Sprintf("%q is not a complete %s version+vendor identifier -- an exact identifier (e.g. %q) is required in --format=json", arg, tool.DisplayName, "21.0.2-"+names[0])}
}

// resolveVendorAndVersion runs the fixed vendor -> major -> patch
// picker sequence. Vendor is asked first, always, since major/patch
// lists are fetched from that specific vendor's API. Skipped entirely
// for a single-vendor tool, where there's no real choice to present.
func resolveVendorAndVersion(ctx context.Context, tool tooldef.Tool) (registry.Provider, string, error) {
	names := vendorNamesFor(tool.Name)

	var provider registry.Provider
	if len(names) == 1 {
		provider = providersFor(tool.Name)[names[0]]
	} else {
		// The picker shows capitalized names, but the return value
		// must map back to the lowercase name providersFor is keyed
		// by (and embedded in version identifiers).
		displayNames := make([]string, len(names))
		for i, name := range names {
			displayNames[i] = capitalize(name)
		}
		chosenDisplay, err := runPicker(session, fmt.Sprintf("Select %s vendor", tool.DisplayName), displayNames, "")
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

	chosenMajor, err := runPicker(session, fmt.Sprintf("Select %s major version", tool.DisplayName), majors, "")
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

	// ListMajorVersions (Adoptium's own /v3/info/available_releases)
	// is NOT filtered by OS/architecture at all -- it lists every
	// major the vendor has EVER published a build for, on ANY
	// platform. ListPatchVersions below, right after a major is
	// chosen, IS filtered by the real runtime.GOOS/GOARCH. So the
	// major picker can genuinely offer a major that turns out to have
	// zero releases for THIS platform specifically -- confirmed as a
	// real, live case, not a hypothetical: Temurin can 404 on a
	// major's feature_releases endpoint for one platform while other
	// platforms (or other vendors, for the same major) have it.
	//
	// Deliberately fails outright here rather than looping back to
	// re-show the major picker with this one removed (an earlier
	// version of this fix did loop back). Two reasons: it sidesteps a
	// real bug class entirely -- picker.Run's own doc comment names
	// printing a status line right before launching another
	// alt-screen picker as something that gets visually wiped out,
	// which is exactly what a loop-back message risks; and a fresh
	// `sk install <tool>` run already re-shows the full picker if the
	// user wants to keep trying, so the loop bought little for the
	// added complexity of threading a warning through as a picker
	// banner and mutating the majors list across iterations.
	patches, err := provider.ListPatchVersions(ctx, chosenMajor, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		if err == registry.ErrVersionNotFound {
			fmt.Fprintln(session.Out)
			fmt.Fprintln(session.Out, styles.Warning.Render(fmt.Sprintf(
				"\u26a0 No %s %s releases for %s/%s via %s -- pick a different major version",
				tool.DisplayName, chosenMajor, runtime.GOOS, runtime.GOARCH, provider.Name(),
			)))
			return nil, "", fmt.Errorf("no releases found for this platform")
		}
		fmt.Fprintln(session.Out)
		fmt.Fprintln(session.Out, styles.Error.Render(
			fmt.Sprintf("\u2717 Could not list %s %s versions: %s", tool.DisplayName, chosenMajor, err),
		))
		return nil, "", err
	}

	chosenPatch, err := runPicker(session, fmt.Sprintf("Select %s %s minor/patch version", tool.DisplayName, chosenMajor), patches, "")
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
