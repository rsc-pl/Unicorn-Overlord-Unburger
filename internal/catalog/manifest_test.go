package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	content := `{"schema":"uo-unburger.translations.draft-1","locale":"en","canonical_variant":"honorifics","records":[]}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.CanonicalVariant != Honorifics || len(result.Records) != 0 {
		t.Fatalf("unexpected manifest: %+v", result)
	}
}

func TestSparseVariantSelection(t *testing.T) {
	different := "Yes, my lord."
	records := []Record{
		{ID: "synthetic-a", SourceSHA256: strings.Repeat("a", 64), TargetEN: "Yes, Alain-dono.", NoHonorificsEN: &different},
		{ID: "synthetic-b", SourceSHA256: strings.Repeat("b", 64), TargetEN: "We should leave now."},
	}
	m := Manifest{Schema: DraftSchema, Locale: "en", CanonicalVariant: Honorifics, Records: records}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		index         int
		variant, want string
	}{
		{0, Honorifics, "Yes, Alain-dono."},
		{0, NoHonorifics, "Yes, my lord."},
		{1, Honorifics, "We should leave now."},
		{1, NoHonorifics, "We should leave now."},
	}
	for _, test := range tests {
		got, err := m.Records[test.index].TextForVariant(test.variant)
		if err != nil || got != test.want {
			t.Errorf("variant %s: got %q, %v; want %q", test.variant, got, err, test.want)
		}
	}
}

func TestRejectsRedundantOverride(t *testing.T) {
	same := "Hello."
	m := Manifest{Schema: DraftSchema, Locale: "en", CanonicalVariant: Honorifics,
		Records: []Record{{ID: "a", SourceSHA256: strings.Repeat("0", 64), TargetEN: same, NoHonorificsEN: &same}}}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "redundant") {
		t.Fatalf("expected redundant override error, got %v", err)
	}
}

func TestRejectsInvalidRecordAndSchema(t *testing.T) {
	m := Manifest{Schema: DraftSchema, Locale: "en", CanonicalVariant: Honorifics,
		Records: []Record{{ID: "a", SourceSHA256: "bad", TargetEN: "Hello."}}}
	if err := m.Validate(); err == nil {
		t.Fatal("expected hash validation error")
	}
	m.Records[0].SourceSHA256 = strings.Repeat("a", 64)
	m.Records = append(m.Records, m.Records[0])
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate ID rejection, got %v", err)
	}
	m.Records = m.Records[:0]
	m.CanonicalVariant = NoHonorifics
	if err := m.Validate(); err == nil {
		t.Fatal("expected canonical edition rejection")
	}
}

func TestUnsupportedEdition(t *testing.T) {
	if _, err := (Record{}).TextForVariant("both"); err == nil {
		t.Fatal("expected unknown edition error")
	}
}
