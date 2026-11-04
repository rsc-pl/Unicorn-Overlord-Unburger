package layering

import "testing"

func TestResolveClassification(t *testing.T) {
	base := map[string]int64{
		"shared.bin":    100,
		"base_only.bin": 50,
	}
	update := []PatchFile{
		{Path: "shared.bin", Size: 120, UpdatedByPatch: true},
		{Path: "localized_us.bin", Size: 80, UpdatedByPatch: true},
		{Path: "new_asset.bin", Size: 30, UpdatedByPatch: true},
		{Path: "inherited.bin", Size: 70, UpdatedByPatch: false},
	}

	got, err := Resolve(base, update)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := map[string]Backing{
		"shared.bin":       ReplacedByPatch,
		"localized_us.bin": AddedByPatch,
		"new_asset.bin":    AddedByPatch,
		"inherited.bin":    InheritedFromBase,
		"base_only.bin":    InheritedFromBase,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for _, f := range got {
		if want[f.Path] != f.Backing {
			t.Errorf("%s: backing = %s, want %s", f.Path, f.Backing, want[f.Path])
		}
	}
	// Sorted by path.
	for i := 1; i < len(got); i++ {
		if got[i-1].Path >= got[i].Path {
			t.Fatalf("not sorted: %q >= %q", got[i-1].Path, got[i].Path)
		}
	}
}

func TestResolveSizes(t *testing.T) {
	base := map[string]int64{"a.bin": 10}
	update := []PatchFile{{Path: "a.bin", Size: 99, UpdatedByPatch: true}}
	got, err := Resolve(base, update)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Size != 99 || got[0].Backing != ReplacedByPatch {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestResolveRejectsBadInput(t *testing.T) {
	if _, err := Resolve(nil, []PatchFile{{Path: ""}}); err == nil {
		t.Fatal("expected error for empty path")
	}
	dup := []PatchFile{{Path: "x"}, {Path: "x"}}
	if _, err := Resolve(nil, dup); err == nil {
		t.Fatal("expected error for duplicate path")
	}
	neg := []PatchFile{{Path: "x", Size: -1}}
	if _, err := Resolve(nil, neg); err == nil {
		t.Fatal("expected error for negative size")
	}
}
