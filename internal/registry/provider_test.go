package registry

import "testing"

// TestDedupeStrings_RemovesDuplicatesPreservingOrder is a regression
// test for a real, live-reported bug: `sk search java liberica 22`
// showed "22.0.1" twice in a row, both marked "installed" -- confirmed
// to originate from ListPatchVersions building its version list with
// no deduplication at all, so two raw releases that both strip down
// to the same bare patch version (e.g. a vendor-issued rebuild)
// appeared as two separate entries.
func TestDedupeStrings_RemovesDuplicatesPreservingOrder(t *testing.T) {
	in := []string{"22.0.1", "22.0.1", "22", "22.0.2"}
	got := DedupeStrings(in)
	want := []string{"22.0.1", "22", "22.0.2"}

	if len(got) != len(want) {
		t.Fatalf("DedupeStrings(%v) = %v, want %v", in, got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("DedupeStrings(%v)[%d] = %q, want %q", in, i, got[i], w)
		}
	}
}

func TestDedupeStrings_NoDuplicatesUnchanged(t *testing.T) {
	in := []string{"21.0.2", "17.0.3", "11.0.2"}
	got := DedupeStrings(in)
	if len(got) != 3 {
		t.Errorf("expected no change for a list with no duplicates, got: %v", got)
	}
}

func TestDedupeStrings_EmptyInput(t *testing.T) {
	got := DedupeStrings(nil)
	if len(got) != 0 {
		t.Errorf("expected an empty result for nil input, got: %v", got)
	}
}

func TestDedupeStrings_AllDuplicates(t *testing.T) {
	got := DedupeStrings([]string{"8", "8", "8", "8"})
	if len(got) != 1 || got[0] != "8" {
		t.Errorf("expected a single \"8\", got: %v", got)
	}
}
