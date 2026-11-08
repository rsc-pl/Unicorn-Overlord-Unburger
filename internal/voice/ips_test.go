package voice

import (
	"bytes"
	"math/rand"
	"testing"

	"uo-unburger/internal/nso"
)

func TestIPSRoundTripVaryingSizes(t *testing.T) {
	rnd := rand.New(rand.NewSource(20261018))
	for _, tc := range []struct {
		name          string
		origLen       int
		patchedLen    int
		mutationCount int
	}{
		{"equal-small", 512, 512, 20},
		{"extend", 512, 900, 40},
		{"truncate", 900, 512, 40},
		{"big", 70000, 90000, 5000},
		{"no-change", 4096, 4096, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := make([]byte, tc.origLen)
			for i := range orig {
				orig[i] = byte(rnd.Intn(256))
			}
			patched := make([]byte, tc.patchedLen)
			copy(patched, orig)
			for k := 0; k < tc.mutationCount; k++ {
				patched[rnd.Intn(len(patched))] = byte(rnd.Intn(256))
			}
			if tc.name == "extend" {
				for i := tc.origLen; i < len(patched); i++ {
					patched[i] = byte(rnd.Intn(256))
				}
			}
			patch, err := BuildIPS(orig, patched)
			if err != nil {
				t.Fatalf("BuildIPS: %v", err)
			}
			got, err := ApplyIPS(orig, patch)
			if err != nil {
				t.Fatalf("ApplyIPS: %v", err)
			}
			if !bytes.Equal(got, patched) {
				t.Fatalf("round-trip mismatch (got %d bytes, want %d)", len(got), len(patched))
			}
		})
	}
}

func TestIPSCompressesRepeatedRuns(t *testing.T) {
	orig := make([]byte, 4096)
	patched := make([]byte, 4096)
	copy(patched, orig)
	for i := 0; i < 1000; i++ {
		patched[i] = 0x77
	}
	patch, err := BuildIPS(orig, patched)
	if err != nil {
		t.Fatalf("BuildIPS: %v", err)
	}
	// A 1000-byte constant run must not be stored literally.
	if len(patch) > 128 {
		t.Fatalf("RLE run was not compressed: patch is %d bytes", len(patch))
	}
	got, err := ApplyIPS(orig, patch)
	if err != nil {
		t.Fatalf("ApplyIPS: %v", err)
	}
	if !bytes.Equal(got, patched) {
		t.Fatal("round-trip mismatch")
	}
}

func TestIPSNoChange(t *testing.T) {
	orig := bytes.Repeat([]byte{0x5A}, 300)
	patch, err := BuildIPS(orig, append([]byte(nil), orig...))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ApplyIPS(orig, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, orig) {
		t.Fatal("no-change patch altered the image")
	}
}

func TestIPSRejectsBadMagic(t *testing.T) {
	if _, err := ApplyIPS([]byte("nope"), []byte("NOTAPATCH")); err == nil {
		t.Fatal("ApplyIPS accepted a non-patch")
	}
}

func TestIPSRoundTripEmpty(t *testing.T) {
	orig := []byte{}
	patched := []byte("hello")
	patch, err := BuildIPS(orig, patched)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ApplyIPS(orig, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, patched) {
		t.Fatal("round-trip mismatch")
	}
}

// mappedOf returns the loader-visible image of a synthetic NSO.
func mappedOf(t *testing.T, img []byte) []byte {
	t.Helper()
	f, err := nso.Parse(img)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, err := f.MappedImage()
	if err != nil {
		t.Fatalf("mapped image: %v", err)
	}
	return m
}

func TestBuildExefsIPSIsSmallAndLoaderShaped(t *testing.T) {
	img, plan := fixturePlan(t)
	patched, _, err := Apply(img, plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	patch, err := BuildExefsIPS(img, patched)
	if err != nil {
		t.Fatalf("BuildExefsIPS: %v", err)
	}
	if patch.Records != 2 {
		t.Fatalf("records = %d, want 2", patch.Records)
	}
	if patch.Changed != 8 {
		t.Fatalf("payload bytes = %d, want 8", patch.Changed)
	}
	// 5 magic + 2*(3+2) headers + 8 payload + 3 EOF = 26.
	if len(patch.Bytes) != 26 {
		t.Fatalf("patch size = %d, want 26 (a stored-layer diff would be far larger)", len(patch.Bytes))
	}
	// Offsets are uncompressed-NSO file offsets: mapped offset + 0x100.
	if patch.First != 0x1140 || patch.Last <= patch.First {
		t.Fatalf("first/last = %#x/%#x, want 0x1140/…", patch.First, patch.Last)
	}
	if _, _, err := parseIPS(patch.Bytes); err != nil {
		t.Fatalf("parse built patch: %v", err)
	}
	// Loader semantics reproduce the patched mapped image exactly.
	got, err := ApplyExefsIPS(img, patch.Bytes)
	if err != nil {
		t.Fatalf("ApplyExefsIPS: %v", err)
	}
	if want := mappedOf(t, patched); !bytes.Equal(got, want) {
		t.Fatal("ApplyExefsIPS does not reproduce the patched mapped image")
	}
}

func TestBuildExefsIPSRejectsDifferentModuleID(t *testing.T) {
	img, plan := fixturePlan(t)
	patched, _, err := Apply(img, plan)
	if err != nil {
		t.Fatal(err)
	}
	f, err := nso.Parse(patched)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := f.Decompress(nso.Rodata)
	if err != nil {
		t.Fatal(err)
	}
	var other [32]byte
	for i := range other {
		other[i] = byte(0x10 + i)
	}
	foreign := synthNSO(t, ro, other, true)
	if _, err := BuildExefsIPS(img, foreign); err == nil {
		t.Fatal("different module id accepted")
	}
}

func TestBuildExefsIPSRejectsIdenticalImages(t *testing.T) {
	img, _ := fixturePlan(t)
	if _, err := BuildExefsIPS(img, img); err == nil {
		t.Fatal("empty patch accepted")
	}
}

func TestApplyExefsIPSSkipsProtectedHeader(t *testing.T) {
	img, plan := fixturePlan(t)
	patched, _, err := Apply(img, plan)
	if err != nil {
		t.Fatal(err)
	}
	// A record inside the 0x100-byte header must never reach the module image.
	headerOnly := []byte("PATCH")
	headerOnly = be24(headerOnly, 0x40)
	headerOnly = be16(headerOnly, 4)
	headerOnly = append(headerOnly, 0xDE, 0xAD, 0xBE, 0xEF)
	headerOnly = append(headerOnly, 'E', 'O', 'F')
	got, err := ApplyExefsIPS(img, headerOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, mappedOf(t, img)) {
		t.Fatal("a header-only record changed the mapped image")
	}
	_ = patched
}
