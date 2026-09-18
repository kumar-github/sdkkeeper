package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// OutputFormat identifies whether a command should behave normally
// (interactive text, pickers, session.Out) or emit the stable,
// external-facing --format=json contract instead. Kept as its own
// small enum, structurally identical in spirit to ShellFormat
// (output.go) -- a bool would only ever cover two states, and this is
// deliberately NOT the same flag/type as ShellFormat: --shell-format
// is hidden, wrapper-plumbing-only, env/PATH-mutation output;
// --format is the documented, external, discovery/result-reporting
// contract described in the design doc. Design doc §8 is explicit
// that the two are kept permanently separate and must never be
// merged back together.
type OutputFormat int

const (
	// FormatInteractive is the default -- today's existing text/picker
	// behavior, completely unaffected by anything in this file.
	FormatInteractive OutputFormat = iota

	// FormatJSON selects the --format=json contract. Per design doc
	// §6, this is non-interactive by inherent necessity: a command
	// can't emit a single JSON envelope AND draw a bubbletea alt-
	// screen picker at the same time, so every JSON-path command in
	// this package never invokes the picker at all -- an operation
	// that would otherwise need one instead reports version_required
	// immediately (see e.g. resolveUseJSON).
	FormatJSON
)

// outputFormat is set at most once per invocation, by formatFlagValue.Set
// during cobra's flag-parsing pass (before PersistentPreRunE runs) --
// same lifecycle as shellFormatFlag/session in root.go. Every command's
// RunE reads this to decide which path (interactive vs JSON) to take.
var outputFormat OutputFormat

// formatFlagValue implements pflag.Value for --format. Implemented as
// a closed, validated enum from day one (design doc §7): any value
// other than the one currently-legal "json" is a hard, immediate
// parse-time error -- the deliberate OPPOSITE of --shell-format's own
// fail-open behavior (see parseShellFormat's doc comment in root.go).
// That asymmetry is intentional, not an inconsistency: --shell-format
// is internal plumbing a human never types directly, so failing open
// there is the safer choice; --format is the stable, DOCUMENTED,
// externally-facing flag this whole design doc describes, so an
// unrecognized value here is a genuine user mistake that deserves an
// immediate, clear error -- not a silent, surprising fallback to
// interactive behavior the user never asked for.
//
// Deliberately named --format, not --json (design doc §7) -- leaves
// room for a future "text"/"yaml" value without ever needing a
// breaking rename.
type formatFlagValue struct{}

func (formatFlagValue) String() string {
	if outputFormat == FormatJSON {
		return "json"
	}
	return ""
}

func (formatFlagValue) Set(raw string) error {
	switch raw {
	case "json":
		outputFormat = FormatJSON
		return nil
	default:
		// Omitting --format entirely never reaches Set at all (pflag
		// only calls Set when the flag is actually given on the
		// command line) -- so this branch is reached ONLY for a
		// genuinely unrecognized value, matching "any unrecognized
		// value is a hard, immediate parse-time error" exactly.
		return fmt.Errorf("unrecognized value %q for --format -- the only currently supported value is \"json\"", raw)
	}
}

func (formatFlagValue) Type() string { return "string" }

// ErrorCode is the closed, Go-typed vocabulary for error.code (design
// doc §5): "all real granularity lives in error.code strings, not
// exit codes." A plain string type, not a bare `string` field on
// jsonError, so every value that can ever be constructed anywhere in
// this package is one of the six named constants below -- there is no
// call site that can accidentally introduce a seventh, undocumented
// code string.
type ErrorCode string

const (
	// ErrCodeInternalError is exit 1 -- any unexpected/unclassified
	// failure (design doc §5's own wording), e.g. an I/O error while
	// reading state that has nothing to do with a specific tool/
	// version/vendor argument the user gave.
	ErrCodeInternalError ErrorCode = "internal_error"

	// ErrCodeVersionRequired is exit 101 -- no version was given
	// where the interactive picker would otherwise have launched, but
	// --format=json can never show one (design doc §6).
	ErrCodeVersionRequired ErrorCode = "version_required"

	// ErrCodeNotFound is exit 102 -- the named tool/vendor/version
	// doesn't exist, or isn't installed/registered.
	ErrCodeNotFound ErrorCode = "not_found"

	// ErrCodeActivationFailed is exit 103 -- the target version was
	// genuinely resolved (found, validated), but the state-changing
	// operation on it then failed: `use`'s RequiresJava prerequisite
	// couldn't be verified, `remove`'s deletion failed, or `default`'s
	// write/clear of its stored-default file failed. Reserved
	// specifically for "we found what we needed to act on, but acting
	// on it blew up" -- an unrelated, wholly unexpected failure (e.g.
	// scanning inventory itself erroring out before any target was
	// even identified) is ErrCodeInternalError instead, not this.
	ErrCodeActivationFailed ErrorCode = "activation_failed"

	// ErrCodeAmbiguousTool is exit 104 -- a malformed/unrecognized
	// tool name (design doc §5's own wording; despite the name, this
	// is sk's existing "unknown tool" case, not a genuinely ambiguous
	// one -- sk has no tool names that collide with each other).
	ErrCodeAmbiguousTool ErrorCode = "ambiguous_tool"

	// ErrCodeDoctorCheckFailed is exit 201 -- `doctor` itself ran
	// fine, but at least one check's own status is "fail" (design doc
	// §4's nuance: this is a FINDING, not an invocation error, so it
	// is paired with a SUCCESS envelope -- see emitDoctorJSON).
	ErrCodeDoctorCheckFailed ErrorCode = "doctor_check_failed"
)

// exitCodeTable is the complete, frozen mapping from design doc §5.
// Deliberately small and coarse -- exit codes wrap at 256, which is
// why HTTP-style codes were explicitly considered and rejected there;
// every command that wants finer-grained detail puts it in
// error.code's message/extra fields instead, never invents a new
// numeric exit code.
var exitCodeTable = map[ErrorCode]int{
	ErrCodeInternalError:     1,
	ErrCodeVersionRequired:   101,
	ErrCodeNotFound:          102,
	ErrCodeActivationFailed:  103,
	ErrCodeAmbiguousTool:     104,
	ErrCodeDoctorCheckFailed: 201,
}

// exitCodeFor looks up code's exit code, defaulting to 1
// (internal_error) for anything not in the table -- defensive only;
// every ErrorCode this package can actually construct has a real
// entry above.
func exitCodeFor(code ErrorCode) int {
	if v, ok := exitCodeTable[code]; ok {
		return v
	}
	return 1
}

// CLIError is what a JSON-path RunE returns instead of a bare error,
// carrying the ErrorCode that determines the process's exit code
// (design doc §5) all the way out to main.go, without main.go (or
// root.go's Execute) needing to know anything about --format=json
// itself. Exported specifically so main.go -- a different package --
// can read it back via ExitCode.
type CLIError struct {
	Code ErrorCode
	Err  error
}

func (e *CLIError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Code)
}

func (e *CLIError) Unwrap() error { return e.Err }

// ExitCode translates whatever error Execute returned into the exact
// process exit code main.go should use. nil -> 0. A *CLIError (from
// any --format=json code path in this package) -> its own code's
// entry in exitCodeTable. Anything else -- every existing, unchanged
// interactive-mode error in this project -- falls back to 1, exactly
// matching main.go's ORIGINAL, unconditional os.Exit(1) behavior, so
// no interactive-mode command's exit code changes at all just because
// this table now exists.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var cliErr *CLIError
	if errors.As(err, &cliErr) {
		return exitCodeFor(cliErr.Code)
	}
	return 1
}

// jsonError is the `error` object inside an error envelope (design
// doc §1). Extra carries any command-specific additional context the
// doc's own sample mentions ("...": "any command-specific extra
// context, e.g. candidates") -- merged into the SAME JSON object as
// code/message via MarshalJSON below, not nested under its own key,
// matching the doc's literal sample shape exactly.
type jsonError struct {
	Code    ErrorCode
	Message string
	Extra   map[string]interface{}
}

func (e jsonError) MarshalJSON() ([]byte, error) {
	m := make(map[string]interface{}, len(e.Extra)+2)
	for k, v := range e.Extra {
		m[k] = v
	}
	m["code"] = string(e.Code)
	m["message"] = e.Message
	return json.Marshal(m)
}

// jsonEnvelope is the one fixed top-level shape every --format=json
// response uses, success or failure, every command, no exceptions
// (design doc §1). Data is omitted entirely from an error response,
// and Error is omitted entirely from a success response -- matching
// the doc's two separate literal samples exactly, neither of which
// carries the other's key at all.
type jsonEnvelope struct {
	SchemaVersion int         `json:"schemaVersion"`
	Status        string      `json:"status"`
	Data          interface{} `json:"data,omitempty"`
	Error         *jsonError  `json:"error,omitempty"`
}

const schemaVersion = 1

// writeJSONEnvelope marshals env and writes it, plus a trailing
// newline, to os.Stdout ONLY -- mirrors writeResult/writeDeactivate's
// own "machine-readable output goes to os.Stdout, human-readable text
// goes to session.Out, never the two mixed on the same stream" rule
// (see output.go), for the identical reason: a caller parsing
// --format=json output needs stdout to contain EXACTLY one JSON value
// and nothing else.
func writeJSONEnvelope(env jsonEnvelope) {
	data, err := json.Marshal(env)
	if err != nil {
		// Marshaling this package's own, fully-controlled envelope
		// types should never actually fail -- but if it somehow does
		// (e.g. a future command's Data payload contains something
		// unmarshalable), emit a bare, hand-built minimal error
		// envelope rather than silently producing no output at all on
		// this stream.
		fmt.Fprintln(os.Stdout, `{"schemaVersion":1,"status":"error","error":{"code":"internal_error","message":"failed to encode response"}}`)
		return
	}
	fmt.Fprintln(os.Stdout, string(data))
}

// emitJSON is the standard success/error emitter shared by every
// --format=json command whose result is a single request/response
// (use, remove, default, list, search, vendors, current) -- write the
// envelope, then return an error carrying the right ErrorCode so
// ExitCode (via main.go) picks the correct process exit code.
// doctor is the one exception (see emitDoctorJSON) -- design doc §4's
// own nuance means a "fail" finding there must NOT flip the envelope
// itself to an error status.
func emitJSON(data interface{}, jerr *jsonError) error {
	if jerr != nil {
		writeJSONEnvelope(jsonEnvelope{SchemaVersion: schemaVersion, Status: "error", Error: jerr})
		return &CLIError{Code: jerr.Code, Err: errors.New(jerr.Message)}
	}
	writeJSONEnvelope(jsonEnvelope{SchemaVersion: schemaVersion, Status: "ok", Data: data})
	return nil
}

// activationAction is the closed, Go-typed enum for `use`/`remove`/
// `default`'s shared "action" field (design doc §2) -- never a free
// string, so every value ever written is one of these four.
type activationAction string

const (
	actionActivated      activationAction = "activated"
	actionRemoved        activationAction = "removed"
	actionDefaulted      activationAction = "defaulted"
	actionDefaultCleared activationAction = "default_cleared"
)

// activationPayload is the shared success `data` shape for `use`,
// `remove`, and `default` (design doc §2) -- one struct, not three
// near-identical ones, since the doc gives all three the exact same
// fields. Vendor is a pointer so it serializes as JSON null, not an
// empty string, when there's none to report (see vendorOf).
type activationPayload struct {
	Tool    string  `json:"tool"`
	Version string  `json:"version"`
	Vendor  *string `json:"vendor"`
	Action  string  `json:"action"`
}

// vendorOf returns the vendor name for a version of toolName, as a
// pointer so it serializes as JSON null (not an empty string) when
// there's no vendor to report -- matching design doc §9 exactly:
// "vendor is null for single-vendor tools", generalized here to also
// cover a managed, multi-vendor version whose suffix doesn't match
// any of the tool's known vendors (an "Other"-bucket entry in list's
// own grouping -- see versionVendor's callers) -- still null, since
// there's no verified vendor name to report, not a guess.
func vendorOf(toolName, version string) *string {
	names := vendorNamesFor(toolName)
	if len(names) < 2 {
		return nil
	}
	if vendor, ok := versionVendor(version, names); ok {
		return &vendor
	}
	return nil
}
