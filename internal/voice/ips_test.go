package voice

import (
	"bytes"
	"math/rand"
	"testing"
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
