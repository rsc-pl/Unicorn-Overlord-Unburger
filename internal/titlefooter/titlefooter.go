// Package titlefooter renders the build-time attribution footer into the
// title-screen credit texture.
//
// The title screen carries a small, vertically set credit mark baked into the
// first surface of one GUI texture atlas. This package replaces that mark, at
// build time and from the user's own extracted texture, with the two-line footer
//
//	UNBURGER MOD
//	<version from version.txt>
//
// so the footer always tracks the release version and is never hard-coded.
//
// The package is deliberately narrow. It understands one texture container
// layout (the FTEX atlas used by the game's GUI textures), edits one fixed,
// block-aligned rectangle of one BC7 surface, and leaves every other byte of the
// input untouched. Text is set in an embedded TrueType typeface and rasterised
// deterministically with antialiased coverage and a dark outline. It contains no
// game data: the only asset-specific facts are the geometry of the credit strip,
// which are recorded here as constants.
package titlefooter

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// MemberPath is the CPK member that carries the title-screen credit mark.
const MemberPath = "GUI/Opening100.ftx"

// Credit strip geometry in the first surface of MemberPath, in pixels.
//
// The mark is set vertically in the atlas: its reading direction runs along the
// atlas +Y axis and its glyph height along +X. The rectangle below is the
// block-aligned bounding box of the mark (x and y are multiples of four, and w
// and h are multiples of four), so the edit touches whole 4x4 BC7 blocks only.
const (
	footerX = 1936
	footerY = 660
	footerW = 40
	footerH = 204
)

// bc7Format is the FTEX surface format code for BC7 (four 8-bit channels,
// 16 bytes per 4x4 block).
const bc7Format = 0x4D

// blockSize is the byte size of one BC7 block.
const blockSize = 16

var (
	// ErrNotFTX reports input that does not begin with the FTEX magic.
	ErrNotFTX = errors.New("titlefooter: not an FTEX texture")
	// ErrUnsupported reports an FTEX layout this package does not edit.
	ErrUnsupported = errors.New("titlefooter: unsupported FTEX layout")
	// ErrVersionMissing reports an empty version string.
	ErrVersionMissing = errors.New("titlefooter: empty version")
)

// Surface describes one BC7 surface of an FTEX atlas.
type Surface struct {
	Index int
	// Width and Height are the surface dimensions in pixels.
	Width, Height int
	// Format is the FTEX format code (bc7Format for an editable surface).
	Format int
	// PayloadOffset is the byte offset of the first swizzled block.
	PayloadOffset int
	// PayloadSize is the byte size of the swizzled block data.
	PayloadSize int
}

// Atlas is a parsed FTEX texture. The parsed metadata only describes where the
// surfaces live; the payload bytes stay in the caller's slice.
type Atlas struct {
	// Textures lists the surfaces in file order.
	Textures []Surface
}

// Parse reads the FTEX directory of an atlas. It validates every surface's
// declared payload against the buffer, so a truncated or padded file is
// rejected instead of being edited at the wrong offset.
func Parse(data []byte) (*Atlas, error) {
	if len(data) < 16 || !bytes.Equal(data[:4], []byte("FTEX")) {
		return nil, ErrNotFTX
	}
	var textures []Surface
	search := 0
	for {
		marker := bytes.Index(data[search:], []byte("FTX0"))
		if marker < 0 {
			break
		}
		off := search + marker
		search = off + 4
		// The tag is followed by a fixed eight-byte prefix and then the
		// per-surface name area. A file that ends inside that prefix is
		// truncated; indexing past it would panic instead of reporting a
		// validation error.
		if off+8 > len(data) {
			return nil, fmt.Errorf("%w: surface tag at %d is truncated", ErrUnsupported, off)
		}
		// The per-surface name area precedes the ".tex" descriptor; the
		// descriptor begins immediately after the four-byte tag.
		desc := bytes.Index(data[off+8:], []byte(".tex"))
		if desc < 0 || desc > 256 {
			continue
		}
		tex := off + 8 + desc
		if tex+4+8*4 > len(data) {
			return nil, fmt.Errorf("%w: surface descriptor at %d is truncated", ErrUnsupported, tex)
		}
		words := make([]uint32, 8)
		for i := range words {
			words[i] = binary.LittleEndian.Uint32(data[tex+4+i*4:])
		}
		width, height := int(words[2]), int(words[3])
		if width <= 0 || height <= 0 {
			return nil, fmt.Errorf("%w: surface %d has %dx%d pixels", ErrUnsupported, len(textures), width, height)
		}
		payload := tex + 0x100
		if payload < 0 || payload > len(data) {
			return nil, fmt.Errorf("%w: surface %d payload starts at %d, past the %d-byte file",
				ErrUnsupported, len(textures), payload, len(data))
		}
		// The payload is stored in Tegra block-linear GOBs. Its allocation
		// covers whole 8x16-block GOB rows, so the required size can exceed the
		// naive widthBlocks*heightBlocks count when a dimension is not a
		// multiple of the GOB geometry. Validate the layout size actually
		// addressed by addrBlockLinear, using division to avoid overflow on
		// malformed dimensions.
		widthBlocks, heightBlocks := blocksWide(width), blocksHigh(height)
		widthInGOBs := (widthBlocks*blockSize + 63) / 64
		gobRows := (heightBlocks + 8*blockHeightBlocks - 1) / (8 * blockHeightBlocks)
		avail := int64(len(data) - payload)
		gobBytes := int64(512*blockHeightBlocks) * int64(widthInGOBs)
		if widthInGOBs <= 0 || gobRows <= 0 || gobBytes <= 0 ||
			gobBytes > avail || int64(gobRows) > avail/gobBytes {
			return nil, fmt.Errorf("%w: surface %d block-linear payload does not fit in the %d-byte file",
				ErrUnsupported, len(textures), len(data))
		}
		size := int(int64(gobRows) * gobBytes)
		textures = append(textures, Surface{
			Index:         len(textures),
			Width:         width,
			Height:        height,
			Format:        int(words[0]),
			PayloadOffset: payload,
			PayloadSize:   size,
		})
	}
	if len(textures) == 0 {
		return nil, fmt.Errorf("%w: no surfaces", ErrUnsupported)
	}
	return &Atlas{Textures: textures}, nil
}

func blocksWide(pixels int) int { return (pixels + 3) / 4 }
func blocksHigh(pixels int) int { return (pixels + 3) / 4 }

// addrBlockLinear returns the byte offset of block (x,y) in a Tegra block-linear
// surface holding 16-byte BC7 blocks. widthBlocks counts 4x4 blocks; blockHeight
// is the GOB height in blocks (16 for these atlases). The mapping was validated
// against an independent decoder of the same atlas.
func addrBlockLinear(x, y, widthBlocks, blockHeight int) int {
	const bpp = blockSize
	widthInGOBs := (widthBlocks*bpp + 63) / 64
	gob := (y/(8*blockHeight))*(512*blockHeight*widthInGOBs) +
		((x*bpp)/64)*(512*blockHeight) +
		((y%(8*blockHeight))/8)*512
	xb := x * bpp
	return gob + ((xb%64)/32)*256 + ((y%8)/2)*64 + ((xb%32)/16)*32 + (y%2)*16 + (xb % 16)
}

// blockHeightBlocks is the GOB height, in blocks, of the title atlases.
const blockHeightBlocks = 16

// region is the validated block-aligned credit strip.
type region struct {
	X, Y, W, H       int
	BlockX, BlockY   int
	BlocksW, BlocksH int
}

func creditRegion() region {
	return region{
		X: footerX, Y: footerY, W: footerW, H: footerH,
		BlockX:  footerX / 4,
		BlockY:  footerY / 4,
		BlocksW: footerW / 4,
		BlocksH: footerH / 4,
	}
}

// Proof records what an Apply call changed, for build reports and tests.
type Proof struct {
	// Surface is the edited surface index (always 0 for the credit mark).
	Surface int
	// X, Y, W, H is the edited rectangle in pixels.
	X, Y, W, H int
	// Blocks is the number of 4x4 blocks the rectangle covers.
	Blocks int
	// ChangedBlocks is how many of those blocks differ from the source.
	ChangedBlocks int
	// ChangedBytes is the number of payload bytes that differ.
	ChangedBytes int
	// MaxChannelError is the largest per-channel difference between the intended
	// antialiased texel and its BC7 encoding. Encoding is lossy by construction;
	// this records the measured error so a bad encode cannot pass silently.
	MaxChannelError int
	// OutsideUnchanged reports that every byte outside the rectangle's blocks
	// is identical to the input.
	OutsideUnchanged bool
	// Typography is the exact configuration used for this render.
	FontSize    float64
	OutlineSize float64
	LineSpacing float64
	OffsetX     float64
	OffsetY     float64
}

// maxEncodedChannelError bounds the accepted BC7 quantisation error for the
// antialiased mark. The codec is lossy; a larger error means the encoder failed
// on a block and the result must not be written.
//
// Mode 4 has two endpoints per single subset, i.e. four 2-bit colour levels.
// A block that contains both the canonical white fill and the dark outline must
// span their range, so the largest possible colour error is about half one
// level interval, (255-12)/6 = 40.5, plus endpoint quantisation. Alpha uses the
// finer 3-bit plane and stays below 20. A bound of 44 therefore accepts the
// legitimate mode-4 colour limit while still catching an encoder regression.
const maxEncodedChannelError = 44

// Apply renders the footer for version into a copy of the atlas, using the
// authoritative embedded typography configuration. It is the normal build path.
// A missing or malformed configuration is an error; there is no fallback.
func Apply(data []byte, version string) ([]byte, Proof, error) {
	cfg, err := EmbeddedConfig()
	if err != nil {
		return nil, Proof{}, err
	}
	return ApplyWithConfig(data, version, cfg)
}

// ApplyWithConfig renders the footer for version with an explicit configuration.
// The input slice is not modified.
func ApplyWithConfig(data []byte, version string, cfg *Config) ([]byte, Proof, error) {
	if err := cfg.Validate(); err != nil {
		return nil, Proof{}, err
	}
	version = normalizeVersion(version)
	if version == "" {
		return nil, Proof{}, ErrVersionMissing
	}
	atlas, err := Parse(data)
	if err != nil {
		return nil, Proof{}, err
	}
	tex := atlas.Textures[0]
	if tex.Format != bc7Format {
		return nil, Proof{}, fmt.Errorf("%w: surface 0 format 0x%02X is not BC7", ErrUnsupported, tex.Format)
	}
	if tex.Width < footerX+footerW || tex.Height < footerY+footerH {
		return nil, Proof{}, fmt.Errorf("%w: surface 0 is %dx%d, smaller than the credit strip",
			ErrUnsupported, tex.Width, tex.Height)
	}

	strip, _, err := renderStrip(version, *cfg)
	if err != nil {
		return nil, Proof{}, err
	}
	reg := creditRegion()
	widthBlocks := blocksWide(tex.Width)
	payloadEnd := tex.PayloadOffset + tex.PayloadSize

	out := append([]byte(nil), data...)
	changed := 0
	changedBytes := 0
	for by := reg.BlockY; by < reg.BlockY+reg.BlocksH; by++ {
		for bx := reg.BlockX; bx < reg.BlockX+reg.BlocksW; bx++ {
			// Parse validates the whole GOB layout, so every in-surface block
			// address is inside the payload. Keep the check here as a defensive
			// invariant: a malformed geometry must fail as a validation error,
			// never as a slice-bounds panic.
			if pos := tex.PayloadOffset + addrBlockLinear(bx, by, widthBlocks, blockHeightBlocks); pos < tex.PayloadOffset || pos+blockSize > payloadEnd {
				return nil, Proof{}, fmt.Errorf("%w: credit strip block (%d,%d) falls outside surface 0's payload", ErrUnsupported, bx, by)
			}
			var pixels [16]pixel
			for i := 0; i < 4; i++ {
				for j := 0; j < 4; j++ {
					ax := bx*4 + j - reg.X
					ay := by*4 + i - reg.Y
					pixels[i*4+j] = strip[ay*stripWidth+ax]
				}
			}
			block := encodeBC7Block(pixels)
			pos := tex.PayloadOffset + addrBlockLinear(bx, by, widthBlocks, blockHeightBlocks)
			if !bytes.Equal(out[pos:pos+blockSize], block[:]) {
				changed++
			}
			copy(out[pos:pos+blockSize], block[:])
		}
	}

	// Fail closed: re-decode every written block and bound the per-channel error
	// against the intended antialiased texels. A block the encoder could not
	// represent within the tolerance aborts the edit.
	maxErr := 0
	for by := reg.BlockY; by < reg.BlockY+reg.BlocksH; by++ {
		for bx := reg.BlockX; bx < reg.BlockX+reg.BlocksW; bx++ {
			pos := tex.PayloadOffset + addrBlockLinear(bx, by, widthBlocks, blockHeightBlocks)
			var got [16]pixel
			decodeBC7Block(out[pos:pos+blockSize], &got)
			for i := 0; i < 4; i++ {
				for j := 0; j < 4; j++ {
					ax := bx*4 + j - reg.X
					ay := by*4 + i - reg.Y
					want := strip[ay*stripWidth+ax]
					g := got[i*4+j]
					diffs := []int{absDiff(g.A, want.A)}
					// RGB is invisible where alpha is zero; compare it only for
					// texels that contribute to the image.
					if want.A != 0 {
						diffs = append(diffs, absDiff(g.R, want.R), absDiff(g.G, want.G), absDiff(g.B, want.B))
					}
					for _, d := range diffs {
						if d > maxErr {
							maxErr = d
						}
					}
					if maxErr > maxEncodedChannelError {
						return nil, Proof{}, fmt.Errorf("titlefooter: block (%d,%d) pixel (%d,%d) encodes with channel error %d > %d",
							bx, by, i, j, maxErr, maxEncodedChannelError)
					}
				}
			}
		}
	}

	// Confirm nothing outside the strip changed.
	for _, s := range atlas.Textures[1:] {
		if !bytes.Equal(data[s.PayloadOffset:s.PayloadOffset+s.PayloadSize], out[s.PayloadOffset:s.PayloadOffset+s.PayloadSize]) {
			return nil, Proof{}, errors.New("titlefooter: a non-edited surface changed")
		}
	}
	changedBytes = countPayloadDiff(data, out, tex, reg, widthBlocks)

	return out, Proof{
		Surface:          0,
		X:                reg.X,
		Y:                reg.Y,
		W:                reg.W,
		H:                reg.H,
		Blocks:           reg.BlocksW * reg.BlocksH,
		ChangedBlocks:    changed,
		ChangedBytes:     changedBytes,
		MaxChannelError:  maxErr,
		OutsideUnchanged: true,
		FontSize:         cfg.FontSize,
		OutlineSize:      cfg.OutlineSize,
		LineSpacing:      cfg.LineSpacing,
		OffsetX:          cfg.OffsetX,
		OffsetY:          cfg.OffsetY,
	}, nil
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// countPayloadDiff counts differing payload bytes and verifies that every
// difference lies inside the credit strip's blocks.
func countPayloadDiff(before, after []byte, tex Surface, reg region, widthBlocks int) int {
	allowed := make([]bool, tex.PayloadSize)
	for by := reg.BlockY; by < reg.BlockY+reg.BlocksH; by++ {
		for bx := reg.BlockX; bx < reg.BlockX+reg.BlocksW; bx++ {
			pos := addrBlockLinear(bx, by, widthBlocks, blockHeightBlocks)
			for i := 0; i < blockSize; i++ {
				allowed[pos+i] = true
			}
		}
	}
	changed := 0
	base := tex.PayloadOffset
	for i := 0; i < tex.PayloadSize; i++ {
		if before[base+i] != after[base+i] {
			if !allowed[i] {
				// Apply already proved confinement; this is a defensive check.
				panic("titlefooter: edit escaped the credit strip")
			}
			changed++
		}
	}
	return changed
}
