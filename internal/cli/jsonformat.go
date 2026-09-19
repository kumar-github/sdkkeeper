package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"sdkkeeper/internal/tooldef"
)

// OutputFormat selects between normal interactive output (text,
// pickers, session.Out) and the stable, machine-readable --format=json
// contract. Kept separate from ShellFormat (output.go): --shell-format
// is hidden, wrapper-plumbing-only output; --format is the documented,
// external discovery/result contract, and the two must never merge.
type OutputFormat int

const (
	FormatInteractive OutputFormat = iota
	// FormatJSON is non-interactive by necessity -- a command can't
	// emit a JSON envelope and draw a picker at once, so every
	// JSON-path command skips the picker entirely and reports
	// version_required instead when one would otherwise be needed.
	FormatJSON
)

// outputFormat is set at most once per invocation, by formatFlagValue.Set
// during flag parsing (same lifecycle as shellFormatFlag in root.go).
var outputFormat OutputFormat

// formatFlagValue implements pflag.Value for --format as a closed,
// validated enum: any value other than "json" is a hard, immediate
// parse-time error. This is the deliberate opposite of
// --shell-format's fail-open behavior (see parseShellFormat) --
// --shell-format is internal plumbing no user types directly, so
// failing open is safe there; --format is a documented, external flag,
// so a bad value is a genuine user mistake worth erroring on.
//
// Named --format rather than --json to leave room for a future
// "text"/"yaml" value without a breaking rename.
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
		return fmt.Errorf("unrecognized value %q for --format -- the only currently supported value is \"json\"", raw)
	}
}

func (formatFlagValue) Type() string { return "string" }

// ErrorCode is the closed vocabulary for error.code: every response
// carries one of the named constants below, never an ad hoc string.
type ErrorCode string

const (
	// ErrCodeInternalError (exit 1) is any unexpected/unclassified
	// failure unrelated to a specific tool/version/vendor argument.
	ErrCodeInternalError ErrorCode = "internal_error"
	// ErrCodeVersionRequired (exit 101): no version was given where a
	// picker would otherwise be shown, and none can be shown here.
	ErrCodeVersionRequired ErrorCode = "version_required"
	// ErrCodeNotFound (exit 102): the named tool/vendor/version
	// doesn't exist or isn't installed.
	ErrCodeNotFound ErrorCode = "not_found"
	// ErrCodeActivationFailed (exit 103): the target was resolved, but
	// the state-changing operation on it failed (RequiresJava check,
	// deletion, default write/clear). A failure before any target was
	// even identified is ErrCodeInternalError instead.
	ErrCodeActivationFailed ErrorCode = "activation_failed"
	// ErrCodeAmbiguousTool (exit 104): an unrecognized tool name.
	ErrCodeAmbiguousTool ErrorCode = "ambiguous_tool"
	// ErrCodeDoctorCheckFailed (exit 201): doctor ran fine, but at
	// least one check's status is "fail" -- a finding, not an
	// invocation error, so it's paired with a success envelope (see
	// emitDoctorJSON).
	ErrCodeDoctorCheckFailed ErrorCode = "doctor_check_failed"
)

// exitCodeTable is the complete, frozen exit-code mapping. Kept small
// and coarse -- codes wrap at 256 -- with all real detail living in
// error.code strings instead.
var exitCodeTable = map[ErrorCode]int{
	ErrCodeInternalError:     1,
	ErrCodeVersionRequired:   101,
	ErrCodeNotFound:          102,
	ErrCodeActivationFailed:  103,
	ErrCodeAmbiguousTool:     104,
	ErrCodeDoctorCheckFailed: 201,
}

func exitCodeFor(code ErrorCode) int {
	if v, ok := exitCodeTable[code]; ok {
		return v
	}
	return 1
}

// CLIError is what a JSON-path RunE returns instead of a bare error,
// carrying the ErrorCode through to main.go via ExitCode without
// main.go needing any knowledge of --format=json itself.
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

// ExitCode translates Execute's returned error into the process exit
// code: nil -> 0, a *CLIError -> its own table entry, anything else
// (every existing interactive-mode error) -> 1, unchanged from before
// this table existed.
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

// jsonError is the `error` object inside an error envelope. Extra
// carries command-specific context (e.g. candidates) merged into the
// same JSON object as code/message, not nested under its own key.
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
// response uses. Data is omitted on error, Error is omitted on
// success -- neither key appears on the other's response.
type jsonEnvelope struct {
	SchemaVersion int         `json:"schemaVersion"`
	Status        string      `json:"status"`
	Data          interface{} `json:"data,omitempty"`
	Error         *jsonError  `json:"error,omitempty"`
}

const schemaVersion = 1

// writeJSONEnvelope marshals env to os.Stdout only -- human-readable
// text goes to session.Out, so a caller parsing --format=json output
// always finds exactly one JSON value on stdout and nothing else.
func writeJSONEnvelope(env jsonEnvelope) {
	data, err := json.Marshal(env)
	if err != nil {
		// Should never happen for these fully-controlled types; fall
		// back to a minimal hand-built envelope rather than nothing.
		fmt.Fprintln(os.Stdout, `{"schemaVersion":1,"status":"error","error":{"code":"internal_error","message":"failed to encode response"}}`)
		return
	}
	fmt.Fprintln(os.Stdout, string(data))
}

// emitJSON is the shared success/error emitter for every --format=json
// command with a single request/response shape (use, remove, default,
// list, search, vendors, current). doctor is the exception -- see
// emitDoctorJSON -- since a failing check there must not flip the
// envelope itself to an error status.
func emitJSON(data interface{}, jerr *jsonError) error {
	if jerr != nil {
		writeJSONEnvelope(jsonEnvelope{SchemaVersion: schemaVersion, Status: "error", Error: jerr})
		return &CLIError{Code: jerr.Code, Err: errors.New(jerr.Message)}
	}
	writeJSONEnvelope(jsonEnvelope{SchemaVersion: schemaVersion, Status: "ok", Data: data})
	return nil
}

// activationAction is the closed enum for use/remove/default's shared
// "action" field.
type activationAction string

const (
	actionActivated      activationAction = "activated"
	actionRemoved        activationAction = "removed"
	actionDefaulted      activationAction = "defaulted"
	actionDefaultCleared activationAction = "default_cleared"
)

// activationPayload is the shared success `data` shape for use,
// remove, and default. Vendor is a pointer so it serializes as JSON
// null, not an empty string, when there's none to report.
type activationPayload struct {
	Tool    string  `json:"tool"`
	Version string  `json:"version"`
	Vendor  *string `json:"vendor"`
	Action  string  `json:"action"`
}

// vendorOf returns the vendor name for a version of toolName, or nil
// (serializing as JSON null) for a single-vendor tool or a suffix that
// doesn't match any known vendor.
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

// versionRequiredNoTTY is the interactive-path counterpart to
// --format=json's own version_required case: when use/remove would
// need to show a picker (no version given, candidates exist) but no
// real terminal is available, this returns a clean, structured error
// instead of the picker's own low-level "/dev/tty could not be
// opened" message. Reuses the same *CLIError/ErrorCode machinery so
// both paths resolve to the same exit code (101) for the same reason.
func versionRequiredNoTTY(tool tooldef.Tool, verb string) error {
	return &CLIError{
		Code: ErrCodeVersionRequired,
		Err:  fmt.Errorf("a version is required to %s %s -- no interactive terminal is available to show a picker", verb, tool.DisplayName),
	}
}
