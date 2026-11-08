package titlefooter

import (
	"bytes"
	"strings"
	"testing"
)

func mustEmbeddedConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := EmbeddedConfig()
	if err != nil {
		t.Fatalf("EmbeddedConfig: %v", err)
	}
	return cfg
}

func TestEmbeddedConfigIsAuthoritative(t *testing.T) {
	cfg := mustEmbeddedConfig(t)
	want := Config{FontSize: 29, OutlineSize: 3, LineSpacing: 2, OffsetX: 0, OffsetY: 0}
	if *cfg != want {
		t.Fatalf("embedded config = %+v, want %+v", *cfg, want)
	}
	data := ConfigJSON()
	if len(data) == 0 {
		t.Fatal("ConfigJSON is empty")
	}
	again, err := LoadConfig(data)
	if err != nil {
		t.Fatalf("LoadConfig(ConfigJSON): %v", err)
	}
	if *again != want {
		t.Fatalf("round-trip config = %+v", *again)
	}
}

func TestLoadConfigRejectsMissingAndMalformed(t *testing.T) {
	valid := `{"font_size":29,"outline_size":3,"line_spacing":2,"offset_x":0,"offset_y":0}`
	if _, err := LoadConfig([]byte(valid)); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, tc := range []struct{ key, frag string }{
		{"font_size", `"font_size":29,`},
		{"outline_size", `"outline_size":3,`},
		{"line_spacing", `"line_spacing":2,`},
		{"offset_x", `"offset_x":0,`},
		{"offset_y", `,"offset_y":0`},
	} {
		doc := strings.Replace(valid, tc.frag, "", 1)
		if _, err := LoadConfig([]byte(doc)); err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Fatalf("missing %s: err = %v", tc.key, err)
		}
	}
	cases := map[string]string{
		"empty":         ``,
		"not json":      `font_size=29`,
		"unknown key":   `{"font_size":29,"outline_size":3,"line_spacing":2,"offset_x":0,"offset_y":0,"typo":1}`,
		"trailing":      valid + `{}`,
		"null":          `{"font_size":null,"outline_size":3,"line_spacing":2,"offset_x":0,"offset_y":0}`,
		"zero font":     `{"font_size":0,"outline_size":3,"line_spacing":2,"offset_x":0,"offset_y":0}`,
		"negative outl": `{"font_size":29,"outline_size":-1,"line_spacing":2,"offset_x":0,"offset_y":0}`,
		"huge font":     `{"font_size":100000,"outline_size":3,"line_spacing":2,"offset_x":0,"offset_y":0}`,
		"huge offset":   `{"font_size":29,"outline_size":3,"line_spacing":2,"offset_x":99999,"offset_y":0}`,
	}
	for name, doc := range cases {
		if _, err := LoadConfig([]byte(doc)); err == nil {
			t.Fatalf("%s: malformed config accepted", name)
		}
	}
}

func TestApplyConfigBoundsAndDeterminism(t *testing.T) {
	src := buildFTX(t, 2048, 2048)
	cfg := mustEmbeddedConfig(t)
	a, pa, err := ApplyWithConfig(src, "0.2.1-test", cfg)
	if err != nil {
		t.Fatalf("ApplyWithConfig: %v", err)
	}
	b, pb, err := ApplyWithConfig(src, "0.2.1-test", cfg)
	if err != nil {
		t.Fatalf("second ApplyWithConfig: %v", err)
	}
	if !bytes.Equal(a, b) || pa != pb {
		t.Fatal("identical inputs and config produced different output")
	}
	if pa.FontSize != 29 || pa.OutlineSize != 3 || pa.LineSpacing != 2 {
		t.Fatalf("proof typography = %+v", pa)
	}

	// A different size must change the rendered bytes.
	other := *cfg
	other.FontSize = 20
	c, _, err := ApplyWithConfig(src, "0.2.1-test", &other)
	if err != nil {
		t.Fatalf("smaller config: %v", err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("a different font_size must change the footer")
	}

	// Out-of-strip typography must fail, not silently rescale or clip glyphs.
	for name, mutate := range map[string]func(*Config){
		"font too large":     func(c *Config) { c.FontSize = 200 },
		"offset x too large": func(c *Config) { c.OffsetX = 300 },
		"offset y too large": func(c *Config) { c.OffsetY = -300 },
	} {
		bad := *cfg
		mutate(&bad)
		if _, _, err := ApplyWithConfig(src, "0.2.1-test", &bad); err == nil {
			t.Fatalf("%s: out-of-bounds config accepted", name)
		}
	}

	// A malformed embedded document would be reported, never defaulted. The
	// parser path is exercised directly here.
	if _, err := LoadConfig(nil); err == nil {
		t.Fatal("empty config accepted")
	}
}

func TestLayoutUsesConfiguredSize(t *testing.T) {
	cfg := mustEmbeddedConfig(t)
	_, size, err := renderStrip("0.2.1-test", *cfg)
	if err != nil {
		t.Fatalf("renderStrip: %v", err)
	}
	if size != 29 {
		t.Fatalf("rendered size = %d, want configured 29", size)
	}
	// offsets are honoured: shifting the mark changes the rendered strip.
	shifted := *cfg
	shifted.OffsetX = 3
	a, _, err := renderStrip("0.2.1-test", *cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := renderStrip("0.2.1-test", shifted)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatal("strip size changed")
	}
	same := true
	for i := range a {
		if a[i] != b[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("offset_x did not move the mark")
	}
}
