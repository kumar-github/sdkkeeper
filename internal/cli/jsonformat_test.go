package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestFormatFlagValue_Set is the --format enum's own closed-validation
// test (design doc §7): "json" is accepted, anything else is a hard
// error -- the deliberate opposite of parseShellFormat's fail-open
// behavior (see root_test.go's TestParseShellFormat for that
// contrast).
func TestFormatFlagValue_Set(t *testing.T) {
	old := outputFormat
	defer func() { outputFormat = old }()

	var f formatFlagValue
	if err := f.Set("json"); err != nil {
		t.Fatalf("Set(%q) returned an unexpected error: %v", "json", err)
	}
	if outputFormat != FormatJSON {
		t.Errorf("expected outputFormat to become FormatJSON, got %v", outputFormat)
	}

	for _, bad := range []string{"", "text", "yaml", "JSON", "Json"} {
		outputFormat = FormatInteractive
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%q) should have failed (only \"json\" is legal), got nil error", bad)
		}
		if outputFormat != FormatInteractive {
			t.Errorf("a rejected Set(%q) must not mutate outputFormat, got %v", bad, outputFormat)
		}
	}
}

// TestExitCodeTable_MatchesDesignDoc is a direct, literal transcription
// of design doc §5's table -- if this table is ever edited without
// updating the doc (or vice versa), this is the one test that catches
// the drift immediately.
func TestExitCodeTable_MatchesDesignDoc(t *testing.T) {
	cases := map[ErrorCode]int{
		ErrCodeInternalError:     1,
		ErrCodeVersionRequired:   101,
		ErrCodeNotFound:          102,
		ErrCodeActivationFailed:  103,
		ErrCodeAmbiguousTool:     104,
		ErrCodeDoctorCheckFailed: 201,
	}
	for code, want := range cases {
		if got := exitCodeFor(code); got != want {
			t.Errorf("exitCodeFor(%q) = %d, want %d", code, got, want)
		}
	}
}

// TestExitCodeFor_UnknownCodeDefaultsToOne confirms the defensive
// fallback -- every ErrorCode this package can actually construct has
// a real table entry, but an unrecognized one should never panic or
// return 0 (which would look like success).
func TestExitCodeFor_UnknownCodeDefaultsToOne(t *testing.T) {
	if got := exitCodeFor(ErrorCode("something_new")); got != 1 {
		t.Errorf("exitCodeFor of an unknown code = %d, want 1", got)
	}
}

// TestExitCode_NilErrorIsZero and its siblings below confirm
// main.go's ExitCode does exactly what its own doc comment promises:
// nil -> 0, a *CLIError -> its own table entry, anything else -> 1
// (matching main.go's ORIGINAL, unconditional os.Exit(1) for every
// unchanged interactive-mode error).
func TestExitCode_NilErrorIsZero(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Errorf("ExitCode(nil) = %d, want 0", got)
	}
}

func TestExitCode_CLIErrorUsesItsOwnCode(t *testing.T) {
	err := &CLIError{Code: ErrCodeNotFound}
	if got := ExitCode(err); got != 102 {
		t.Errorf("ExitCode(CLIError{not_found}) = %d, want 102", got)
	}
}

func TestExitCode_WrappedCLIErrorStillResolves(t *testing.T) {
	// errors.As must see through a wrapper -- confirms ExitCode uses
	// errors.As, not a bare type assertion, so a future call site that
	// wraps a *CLIError with fmt.Errorf("...: %w", err) still resolves
	// to the right exit code rather than silently falling back to 1.
	inner := &CLIError{Code: ErrCodeActivationFailed}
	wrapped := errors.Join(inner)
	if got := ExitCode(wrapped); got != 103 {
		t.Errorf("ExitCode(wrapped CLIError) = %d, want 103", got)
	}
}

func TestExitCode_OrdinaryErrorFallsBackToOne(t *testing.T) {
	if got := ExitCode(errors.New("some ordinary interactive-mode error")); got != 1 {
		t.Errorf("ExitCode(plain error) = %d, want 1 (matching the original unconditional os.Exit(1))", got)
	}
}

// TestCLIError_ErrorString confirms the human-readable message prefers
// the wrapped Err when present, and falls back to the bare code string
// otherwise -- both are exercised since a *CLIError is sometimes built
// with no wrapped error at all (e.g. tests that only care about Code).
func TestCLIError_ErrorString(t *testing.T) {
	withErr := &CLIError{Code: ErrCodeNotFound, Err: errors.New("JDK-99 not found")}
	if got := withErr.Error(); got != "JDK-99 not found" {
		t.Errorf("expected the wrapped error's message, got %q", got)
	}
	bare := &CLIError{Code: ErrCodeNotFound}
	if got := bare.Error(); got != "not_found" {
		t.Errorf("expected the bare code string as a fallback, got %q", got)
	}
}

// TestJSONEnvelope_SuccessOmitsErrorKey and
// TestJSONEnvelope_ErrorOmitsDataKey are direct transcriptions of
// design doc §1's two literal samples -- a success envelope has NO
// "error" key at all, and an error envelope has NO "data" key at all,
// not merely a null value for either.
func TestJSONEnvelope_SuccessOmitsErrorKey(t *testing.T) {
	env := jsonEnvelope{SchemaVersion: 1, Status: "ok", Data: map[string]string{"tool": "java"}}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	out := string(data)
	if strings.Contains(out, `"error"`) {
		t.Errorf("a success envelope must never contain an \"error\" key, got: %s", out)
	}
	if !strings.Contains(out, `"schemaVersion":1`) {
		t.Errorf("expected schemaVersion:1 present, got: %s", out)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected status:ok, got: %s", out)
	}
}

func TestJSONEnvelope_ErrorOmitsDataKey(t *testing.T) {
	env := jsonEnvelope{SchemaVersion: 1, Status: "error", Error: &jsonError{Code: ErrCodeNotFound, Message: "nope"}}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	out := string(data)
	if strings.Contains(out, `"data"`) {
		t.Errorf("an error envelope must never contain a \"data\" key, got: %s", out)
	}
	if !strings.Contains(out, `"code":"not_found"`) {
		t.Errorf("expected the error code inline, got: %s", out)
	}
	if !strings.Contains(out, `"message":"nope"`) {
		t.Errorf("expected the error message inline, got: %s", out)
	}
}

// TestJSONError_ExtraFieldsMergeIntoSameObject confirms design doc
// §1's "'...': 'any command-specific extra context, e.g. candidates'"
// -- Extra keys land in the SAME JSON object as code/message, not
// nested under their own sub-key.
func TestJSONError_ExtraFieldsMergeIntoSameObject(t *testing.T) {
	e := jsonError{
		Code:    ErrCodeNotFound,
		Message: "unknown vendor: bogus",
		Extra:   map[string]interface{}{"candidates": []string{"temurin", "liberica"}},
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if m["code"] != "not_found" {
		t.Errorf("expected code=not_found at the top level, got: %v", m["code"])
	}
	if m["message"] != "unknown vendor: bogus" {
		t.Errorf("expected message at the top level, got: %v", m["message"])
	}
	if _, ok := m["candidates"]; !ok {
		t.Errorf("expected candidates merged into the same object, got: %v", m)
	}
}

// TestJSONError_NoExtraStillMarshalsCleanly confirms the common case
// (no Extra at all) doesn't emit a stray empty key or fail.
func TestJSONError_NoExtraStillMarshalsCleanly(t *testing.T) {
	e := jsonError{Code: ErrCodeVersionRequired, Message: "a version is required"}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if len(m) != 2 {
		t.Errorf("expected exactly code+message with no Extra, got: %v", m)
	}
}

// TestEmitJSON_SuccessReturnsNilError confirms emitJSON's success path
// (used by use/remove/default/list/search/vendors/current) returns a
// nil error -- process exit code 0, per design doc §5.
func TestEmitJSON_SuccessReturnsNilError(t *testing.T) {
	out := captureStdout(t, func() {
		err := emitJSON(map[string]string{"tool": "java"}, nil)
		if err != nil {
			t.Errorf("expected nil error on success, got: %v", err)
		}
	})
	if !strings.Contains(out, `"status":"ok"`) {
		t.Errorf("expected a success envelope on stdout, got: %s", out)
	}
}

// TestEmitJSON_ErrorReturnsCLIErrorWithMatchingCode confirms emitJSON's
// error path both writes an error envelope AND returns a *CLIError
// carrying the SAME code, so ExitCode(err) resolves correctly all the
// way out to main.go.
func TestEmitJSON_ErrorReturnsCLIErrorWithMatchingCode(t *testing.T) {
	jerr := &jsonError{Code: ErrCodeActivationFailed, Message: "could not remove it"}
	var returned error
	out := captureStdout(t, func() {
		returned = emitJSON(nil, jerr)
	})
	if !strings.Contains(out, `"status":"error"`) {
		t.Errorf("expected an error envelope on stdout, got: %s", out)
	}
	if !strings.Contains(out, `"code":"activation_failed"`) {
		t.Errorf("expected the error code in the envelope, got: %s", out)
	}
	var cliErr *CLIError
	if !errors.As(returned, &cliErr) {
		t.Fatalf("expected emitJSON's error to be a *CLIError, got: %T (%v)", returned, returned)
	}
	if cliErr.Code != ErrCodeActivationFailed {
		t.Errorf("expected CLIError.Code = activation_failed, got %q", cliErr.Code)
	}
	if ExitCode(returned) != 103 {
		t.Errorf("expected ExitCode(returned) = 103, got %d", ExitCode(returned))
	}
}

// TestVendorOf_SingleVendorToolIsAlwaysNil confirms design doc §9:
// "vendor is null for single-vendor tools" -- exercised against a
// real registered single-vendor tool (maven), not a synthetic one.
func TestVendorOf_SingleVendorToolIsAlwaysNil(t *testing.T) {
	if got := vendorOf("maven", "3.9.9"); got != nil {
		t.Errorf("expected nil vendor for a single-vendor tool, got: %v", *got)
	}
}

// TestVendorOf_MultiVendorToolExtractsSuffix confirms the normal,
// recognized-suffix case for a real multi-vendor tool (java).
func TestVendorOf_MultiVendorToolExtractsSuffix(t *testing.T) {
	got := vendorOf("java", "21.0.2-temurin")
	if got == nil || *got != "temurin" {
		t.Errorf("expected vendor \"temurin\", got: %v", got)
	}
}

// TestVendorOf_UnrecognizedSuffixIsNilNotAGuess confirms an
// unrecognized suffix on a multi-vendor tool reports null rather than
// inventing an unverified vendor name (mirrors list's own "Other"
// bucket for exactly this case).
func TestVendorOf_UnrecognizedSuffixIsNilNotAGuess(t *testing.T) {
	if got := vendorOf("java", "8u392"); got != nil {
		t.Errorf("expected nil vendor for an unrecognized suffix, got: %v", *got)
	}
}

// TestVendorOf_UnknownToolIsNil confirms a tool with zero registered
// vendors (or an unrecognized name entirely) never panics and just
// reports nil, since vendorNamesFor already returns an empty slice
// for that case.
func TestVendorOf_UnknownToolIsNil(t *testing.T) {
	if got := vendorOf("not-a-real-tool", "1.0"); got != nil {
		t.Errorf("expected nil vendor for an unknown tool, got: %v", *got)
	}
}
