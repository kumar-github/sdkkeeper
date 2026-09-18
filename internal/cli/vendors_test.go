package cli

import "testing"

func TestBuildVendorsJSON_UnknownToolIsAmbiguousTool(t *testing.T) {
	_, jerr := buildVendorsJSON("not-a-real-tool")
	if jerr == nil || jerr.Code != ErrCodeAmbiguousTool {
		t.Fatalf("expected ambiguous_tool, got %+v", jerr)
	}
}

// TestBuildVendorsJSON_MultiVendorToolReportsOrderedNames confirms
// java's real, deliberate vendor ORDER (temurin before liberica --
// see providersByTool's own doc comment on why this order is
// meaningful, not alphabetical) survives into the JSON output too.
func TestBuildVendorsJSON_MultiVendorToolReportsOrderedNames(t *testing.T) {
	data, jerr := buildVendorsJSON("java")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	want := []string{"temurin", "liberica"}
	if len(data.Vendors) != len(want) {
		t.Fatalf("expected %v, got %v", want, data.Vendors)
	}
	for i, v := range want {
		if data.Vendors[i] != v {
			t.Errorf("expected vendors[%d] = %q, got %q (order matters here)", i, v, data.Vendors[i])
		}
	}
}

// TestBuildVendorsJSON_SingleVendorTool confirms a single-vendor tool
// (maven) reports its one vendor, not an empty list and not an error.
func TestBuildVendorsJSON_SingleVendorTool(t *testing.T) {
	data, jerr := buildVendorsJSON("maven")
	if jerr != nil {
		t.Fatalf("expected success, got error: %+v", jerr)
	}
	if len(data.Vendors) != 1 || data.Vendors[0] != "apache" {
		t.Errorf("expected [\"apache\"], got %v", data.Vendors)
	}
}

// TestBuildVendorsJSON_NoProvidersIsEmptyArrayNotError confirms
// design doc's "valid, non-error state" principle applies here too
// (same reasoning as current's active:null) -- a tool with zero
// registered providers (e.g. "kafka", per providersByTool's own
// current contents) reports an empty array, status ok, not a
// jsonError.
func TestBuildVendorsJSON_NoProvidersIsEmptyArrayNotError(t *testing.T) {
	data, jerr := buildVendorsJSON("kafka")
	if jerr != nil {
		t.Fatalf("expected success (empty vendors is not an error), got error: %+v", jerr)
	}
	if data.Vendors == nil {
		t.Error("expected a non-nil (empty) slice, got nil -- would serialize as JSON null instead of []")
	}
	if len(data.Vendors) != 0 {
		t.Errorf("expected zero vendors for kafka, got %v", data.Vendors)
	}
}
