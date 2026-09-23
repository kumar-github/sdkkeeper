package cli

import "testing"

func TestRemoveString_RemovesFirstOccurrenceOnly(t *testing.T) {
	got := removeString([]string{"27", "21", "17", "21"}, "21")
	want := []string{"27", "17", "21"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestRemoveString_NotFoundLeavesSliceUnchanged(t *testing.T) {
	got := removeString([]string{"27", "21"}, "99")
	if len(got) != 2 || got[0] != "27" || got[1] != "21" {
		t.Fatalf("expected unchanged [27 21], got %v", got)
	}
}

func TestRemoveString_EmptyInputStaysEmpty(t *testing.T) {
	got := removeString(nil, "27")
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}
