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
