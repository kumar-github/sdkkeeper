package cli

import (
	"testing"

	"sdkkeeper/internal/tooldef"
)

// TestToolNames_Alphabetical confirms tools are listed alphabetically
// (java, kafka, maven, node -- gradle sorts before kafka), not
// tooldef.Registry's incidental map iteration order.
func TestToolNames_Alphabetical(t *testing.T) {
	names := toolNames()
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("expected alphabetical order, got %v out of order at %d: %q >= %q", names, i, names[i-1], names[i])
		}
	}
}

// TestVendorsCell_MultiVendorTool confirms java's real, deliberate
// vendor order (temurin before liberica) survives into the table
// cell, capitalized and comma-joined.
func TestVendorsCell_MultiVendorTool(t *testing.T) {
	got := vendorsCell("java")
	want := "Temurin, Liberica"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestVendorsCell_SingleVendorTool confirms a single-vendor tool
// (maven) shows its one real vendor name, not a "(single)" placeholder.
func TestVendorsCell_SingleVendorTool(t *testing.T) {
	got := vendorsCell("maven")
	want := "Apache"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestVendorsCell_NoProviders confirms a tool with zero registered
// providers (e.g. kafka, per providersByTool's own current contents)
// renders as "-", not an empty string or an error.
func TestVendorsCell_NoProviders(t *testing.T) {
	got := vendorsCell("kafka")
	want := "-"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestBuildToolsJSON_IncludesEveryRegisteredTool confirms the JSON
// listing covers every tool in tooldef.Registry, not just those with
// install providers (vendors is a real fact per tool, empty for none
// -- see buildVendorsJSON's own precedent for this exact convention).
func TestBuildToolsJSON_IncludesEveryRegisteredTool(t *testing.T) {
	data := buildToolsJSON()
	if len(data.Tools) != len(tooldef.Registry) {
		t.Fatalf("expected %d tools, got %d: %+v", len(tooldef.Registry), len(data.Tools), data.Tools)
	}
}

// TestBuildToolsJSON_VendorsNeverNil confirms every entry's Vendors
// serializes as [] rather than null for a no-provider tool, matching
// buildVendorsJSON's own non-nil-empty-slice convention.
func TestBuildToolsJSON_VendorsNeverNil(t *testing.T) {
	data := buildToolsJSON()
	for _, entry := range data.Tools {
		if entry.Vendors == nil {
			t.Errorf("tool %q: expected non-nil (possibly empty) Vendors slice, got nil", entry.Name)
		}
	}
}

// TestBuildToolsJSON_MultiVendorOrderPreserved confirms java's
// deliberate vendor order (temurin before liberica) survives into
// the JSON path too, same invariant as
// TestBuildVendorsJSON_MultiVendorToolReportsOrderedNames.
func TestBuildToolsJSON_MultiVendorOrderPreserved(t *testing.T) {
	data := buildToolsJSON()
	for _, entry := range data.Tools {
		if entry.Name != "java" {
			continue
		}
		want := []string{"temurin", "liberica"}
		if len(entry.Vendors) != len(want) {
			t.Fatalf("expected %v, got %v", want, entry.Vendors)
		}
		for i, v := range want {
			if entry.Vendors[i] != v {
				t.Errorf("expected vendors[%d] = %q, got %q", i, v, entry.Vendors[i])
			}
		}
		return
	}
	t.Fatal("java not found in tools JSON")
}
