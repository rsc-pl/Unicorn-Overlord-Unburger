package titlefooter

// BC7 codec for a single 4x4 block.
//
// The footer art is white type inside a dark outline on a transparent field.
// Two block layouts are used:
//
//   - Mode 6 for blocks that contain only the canonical white and transparent
//     texels. Its one-subset RGBA endpoints hold both colours exactly.
//   - Mode 4 for every other block. Mode 4 keeps colour and alpha in separate
//     planes, so coverage-based fill/outline compositing is representable: the
//     white-to-dark blend uses the four 2-bit colour levels while alpha uses the
//     finer eight-level 3-bit plane. A block that contains both the white fill
//     and the dark outline must span their range, so its colour error is bounded
//     by roughly half one colour level; see maxEncodedChannelError.
//
// Mode 6's 128-bit layout, least significant bit first, is:
//
//	mode prefix  6 zero bits then one (7 bits)
//	endpoints    R0 R1 G0 G1 B0 B1 A0 A1, 7 bits each (56 bits)
//	parity bits  P0 P1, one bit each; the parity bit is each endpoint's LSB
//	indices      texel 0 in 3 bits (MSB implied zero, the fix-up index), then
//	             texels 1..15 in 4 bits each (63 bits)
//
// Mode 4's layout is:
//
//	mode prefix  4 zero bits then one (5 bits)
//	rotation     2 bits (0 keeps RGBA order)
//	index mode   1 bit (0 selects 2-bit colour and 3-bit alpha indices)
//	colour       R0 R1 G0 G1 B0 B1, 5 bits each (30 bits)
//	alpha        A0 A1, 6 bits each (12 bits)
//	colour idx   texel 0 in 1 bit (MSB implied zero), texels 1..15 in 2 bits
//	alpha idx    texel 0 in 2 bits (MSB implied zero), texels 1..15 in 3 bits
//
// Both decoders below follow the published BC7 specification; the mode-6
// decoder was checked against an independent decoder of the same atlas, and the
// mode-4 layout was checked against a reference decoder implementation. Apply
// re-decodes every block it writes, so an encoding mistake fails the build.

// bc7Weights4 are the 16 mode-6 interpolation weights, in 64ths.
var bc7Weights4 = [16]int{0, 4, 9, 13, 17, 21, 26, 30, 34, 38, 43, 47, 51, 55, 60, 64}

// bc7Weights2 and bc7Weights3 are the 2-bit and 3-bit interpolation weights.
var (
	bc7Weights2 = [4]int{0, 21, 43, 64}
	bc7Weights3 = [8]int{0, 9, 18, 27, 37, 46, 55, 64}
)

// bitReader reads a little-endian bit stream such as a BC7 block.
type bitReader struct {
	data []byte
	pos  int
}

func (r *bitReader) bit() int {
	i := r.pos
	r.pos++
	return int(r.data[i>>3]>>(i&7)) & 1
}

func (r *bitReader) bits(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		v |= r.bit() << i
	}
	return v
}

// readMode consumes the unary mode prefix and returns the block mode. A block
// that never asserts a mode bit decodes as reserved (8).
func readMode(r *bitReader) int {
	mode := 0
	for mode < 8 && r.bit() == 0 {
		mode++
	}
	return mode
}

// decodeBC7Block decodes one block of either layout into 16 RGBA texels. An
// unsupported or reserved mode decodes as transparent black.
func decodeBC7Block(block []byte, out *[16]pixel) {
	*out = [16]pixel{}
	r := &bitReader{data: block}
	switch readMode(r) {
	case 4:
		decodeMode4Body(r, out)
	case 6:
		decodeMode6Body(r, out)
	}
}

// decodeBC7Mode6 decodes one 16-byte mode-6 block into 16 RGBA texels.
func decodeBC7Mode6(block []byte, out *[16]pixel) {
	*out = [16]pixel{}
	r := &bitReader{data: block}
	if readMode(r) != 6 {
		return
	}
	decodeMode6Body(r, out)
}

func decodeMode6Body(r *bitReader, out *[16]pixel) {
	var ep [2][4]int
	for c := 0; c < 3; c++ {
		for e := 0; e < 2; e++ {
			ep[e][c] = r.bits(7)
		}
	}
	for e := 0; e < 2; e++ {
		ep[e][3] = r.bits(7)
	}
	for e := 0; e < 2; e++ {
		p := r.bit()
		for c := 0; c < 4; c++ {
			ep[e][c] = (ep[e][c] << 1) | p
		}
	}
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			bits := 4
			if i == 0 && j == 0 {
				bits = 3 // fix-up index: the MSB is implied zero
			}
			w := bc7Weights4[r.bits(bits)]
			out[i*4+j] = pixel{
				R: uint8(lerp64(ep[0][0], ep[1][0], w)),
				G: uint8(lerp64(ep[0][1], ep[1][1], w)),
				B: uint8(lerp64(ep[0][2], ep[1][2], w)),
				A: uint8(lerp64(ep[0][3], ep[1][3], w)),
			}
		}
	}
}

// decodeBC7Mode4 decodes one 16-byte mode-4 block into 16 RGBA texels.
func decodeBC7Mode4(block []byte, out *[16]pixel) {
	*out = [16]pixel{}
	r := &bitReader{data: block}
	if readMode(r) != 4 {
		return
	}
	decodeMode4Body(r, out)
}

func decodeMode4Body(r *bitReader, out *[16]pixel) {
	rotation := r.bits(2)
	indexMode := r.bit()

	var ce [2][3]int
	for c := 0; c < 3; c++ {
		for e := 0; e < 2; e++ {
			ce[e][c] = r.bits(5)
		}
	}
	var ae [2]int
	ae[0] = r.bits(6)
	ae[1] = r.bits(6)

	var cidx, aidx [16]int
	if indexMode == 0 {
		cidx[0] = r.bits(1)
		for i := 1; i < 16; i++ {
			cidx[i] = r.bits(2)
		}
		aidx[0] = r.bits(2)
		for i := 1; i < 16; i++ {
			aidx[i] = r.bits(3)
		}
	} else {
		// The index-mode bit swaps the two index planes.
		aidx[0] = r.bits(1)
		for i := 1; i < 16; i++ {
			aidx[i] = r.bits(2)
		}
		cidx[0] = r.bits(2)
		for i := 1; i < 16; i++ {
			cidx[i] = r.bits(3)
		}
	}

	for e := 0; e < 2; e++ {
		for c := 0; c < 3; c++ {
			ce[e][c] = dequant(ce[e][c], 5)
		}
		ae[e] = dequant(ae[e], 6)
	}

	colourWeights, alphaWeights := bc7Weights2[:], bc7Weights3[:]
	if indexMode == 1 {
		colourWeights, alphaWeights = bc7Weights3[:], bc7Weights2[:]
	}

	for i := range out {
		p := pixel{
			R: uint8(lerp64(ce[0][0], ce[1][0], colourWeights[cidx[i]])),
			G: uint8(lerp64(ce[0][1], ce[1][1], colourWeights[cidx[i]])),
			B: uint8(lerp64(ce[0][2], ce[1][2], colourWeights[cidx[i]])),
			A: uint8(lerp64(ae[0], ae[1], alphaWeights[aidx[i]])),
		}
		// Rotation re-labels one colour channel as the scalar plane.
		if rotation != 0 {
			ch := channelPtr(&p, rotation-1)
			p.A, *ch = *ch, p.A
		}
		out[i] = p
	}
}

// lerp64 interpolates two 8-bit endpoints with a weight in 64ths.
func lerp64(a, b, w int) int {
	return (a*(64-w) + b*w + 32) >> 6
}

// dequant expands a BC7 endpoint of n bits to eight bits by replicating the
// high bits into the low bits, as the specification requires.
func dequant(v, n int) int {
	v <<= (8 - n)
	return v | (v >> n)
}

// bitWriter accumulates a little-endian bit stream.
type bitWriter struct {
	data []byte
	pos  int
}

func (w *bitWriter) bit(v int) {
	if w.pos>>3 >= len(w.data) {
		w.data = append(w.data, 0)
	}
	if v&1 != 0 {
		w.data[w.pos>>3] |= 1 << (w.pos & 7)
	}
	w.pos++
}

func (w *bitWriter) bits(v, n int) {
	for i := 0; i < n; i++ {
		w.bit(v >> i)
	}
}

// encodeBC7Block encodes 16 RGBA texels as one BC7 block. Blocks made only of
// canonical white and transparent texels use mode 6, which is exact; all other
// blocks use mode 4, whose separate alpha plane represents the antialiased dark
// outline faithfully.
func encodeBC7Block(px [16]pixel) [16]byte {
	exact := true
	for _, p := range px {
		if p != white && p != transparent {
			exact = false
			break
		}
	}
	if exact {
		return encodeBC7Mode6(px)
	}
	return encodeBC7Mode4(px)
}

// encodeBC7Mode6 encodes 16 RGBA texels as one mode-6 block. It is exact for
// canonical white/transparent art; other content goes through fitEndpoints and
// may be quantised.
func encodeBC7Mode6(px [16]pixel) [16]byte {
	// The footer art is white on transparent; that palette encodes exactly, so
	// use the canonical endpoint pair instead of a fitted one.
	exact := true
	for _, p := range px {
		if p != white && p != transparent {
			exact = false
			break
		}
	}
	var e [2][4]int
	var idx [16]int
	if exact {
		// endpoint 0 = transparent black (index 0), endpoint 1 = white (index 15)
		for c := 0; c < 4; c++ {
			e[1][c] = 255
		}
		for i, p := range px {
			if p == white {
				idx[i] = 15
			}
		}
	} else {
		e, idx = fitEndpoints(px)
	}

	// Texel 0 is the fix-up index and stores only three of its four bits, so its
	// index must be 0..7. The weight table is symmetric (w[15-i] == 64-w[i]), so
	// swapping the endpoints and complementing every index is exact.
	if idx[0] > 7 {
		e[0], e[1] = e[1], e[0]
		for i := range idx {
			idx[i] = 15 - idx[i]
		}
	}

	// Quantise to 7 bits plus a parity bit and recompute the indices against the
	// quantised endpoints, so the encoder and decoder agree exactly.
	var q [2][4]int
	var parity [2]int
	for j := 0; j < 2; j++ {
		for c := 0; c < 4; c++ {
			v := clamp8(e[j][c])
			q[j][c] = v >> 1
			parity[j] |= v & 1
		}
	}
	qe := [2][4]int{}
	for j := 0; j < 2; j++ {
		for c := 0; c < 4; c++ {
			qe[j][c] = (q[j][c] << 1) | (parity[j] & 1)
		}
	}
	for i, p := range px {
		idx[i] = nearestIndex4(qe, p)
	}
	if idx[0] > 7 {
		// Defensive: keep the fix-up texel representable in three bits.
		q[0], q[1] = q[1], q[0]
		parity[0], parity[1] = parity[1], parity[0]
		for i := range idx {
			idx[i] = 15 - idx[i]
		}
	}

	var w bitWriter
	w.bits(0, 6)
	w.bit(1)
	for c := 0; c < 3; c++ {
		for j := 0; j < 2; j++ {
			w.bits(q[j][c], 7)
		}
	}
	for j := 0; j < 2; j++ {
		w.bits(q[j][3], 7)
	}
	for j := 0; j < 2; j++ {
		w.bit(parity[j])
	}
	for i := 0; i < 16; i++ {
		if i == 0 {
			w.bits(idx[i], 3)
		} else {
			w.bits(idx[i], 4)
		}
	}
	var out [16]byte
	copy(out[:], w.data)
	return out
}

// encodeBC7Mode4 encodes 16 RGBA texels as one mode-4 block with rotation 0 and
// the default index mode (2-bit colour, 3-bit alpha).
//
// Anti-aliased fill/outline compositing produces texels along the line between
// the white fill and the dark outline, so a block can contain four distinct
// colour levels rather than two flat colours. Colour endpoints are fitted by
// least squares over the block's visible texels and every 2-bit colour index is
// used; alpha endpoints span the block's alpha range and are matched with the
// eight-level 3-bit weight table.
func encodeBC7Mode4(px [16]pixel) [16]byte {
	var e [2][3]int
	var cidx [16]int
	visible := 0
	for _, p := range px {
		if p.A != 0 {
			visible++
		}
	}
	if visible > 0 {
		// Seed the endpoints from the per-channel range of the visible texels.
		for c := 0; c < 3; c++ {
			lo, hi := 255, 0
			for _, p := range px {
				if p.A == 0 {
					continue
				}
				v := channel(p, c)
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
			e[0][c], e[1][c] = lo, hi
		}
		for pass := 0; pass < 2; pass++ {
			for i, p := range px {
				if p.A == 0 {
					cidx[i] = 0
					continue
				}
				cidx[i] = nearestColourIndex(e, p)
			}
			// Least-squares refinement per channel over the chosen 2-bit weights.
			for c := 0; c < 3; c++ {
				var saa, sab, sbb, sap, sbp float64
				for i, p := range px {
					if p.A == 0 {
						continue
					}
					w := float64(bc7Weights2[cidx[i]]) / 64
					a, b := 1-w, w
					v := float64(channel(p, c))
					saa += a * a
					sab += a * b
					sbb += b * b
					sap += a * v
					sbp += b * v
				}
				det := saa*sbb - sab*sab
				if det < 1e-9 {
					continue
				}
				e0 := clamp8(int((sap*sbb-sbp*sab)/det + 0.5))
				e1 := clamp8(int((saa*sbp-sab*sap)/det + 0.5))
				if e0 > e1 {
					e0, e1 = e1, e0
				}
				// Keep the endpoints outside the block's actual range so the
				// canonical white and dark texels stay on the first and last
				// level instead of being pulled in by the least-squares fit.
				lo, hi := 255, 0
				for _, p := range px {
					if p.A == 0 {
						continue
					}
					v := channel(p, c)
					if v < lo {
						lo = v
					}
					if v > hi {
						hi = v
					}
				}
				if e0 > lo {
					e0 = lo
				}
				if e1 < hi {
					e1 = hi
				}
				e[0][c], e[1][c] = e0, e1
			}
		}
	}

	// Quantise colour endpoints to five bits per channel, then recompute every
	// index against the decoded endpoints so the encoder and decoder agree.
	var qc [2][3]int
	for g := 0; g < 2; g++ {
		for c := 0; c < 3; c++ {
			qc[g][c] = quantise(clamp8(e[g][c]), 5)
		}
	}
	var dc [2][3]int
	for g := 0; g < 2; g++ {
		for c := 0; c < 3; c++ {
			dc[g][c] = dequant(qc[g][c], 5)
		}
	}
	for i, p := range px {
		if p.A == 0 {
			cidx[i] = 0
			continue
		}
		cidx[i] = nearestColourIndex(dc, p)
	}
	// The fix-up texel (0) stores only one colour-index bit, so it may only hold
	// index 0 or 1. Swapping the endpoints and complementing every index is
	// exact because the 2-bit weights are symmetric.
	if cidx[0] > 1 {
		qc[0], qc[1] = qc[1], qc[0]
		for i := range cidx {
			cidx[i] = 3 - cidx[i]
		}
	}

	// Alpha endpoints span the block; fit indices against the decoded endpoints.
	lo, hi := 255, 0
	for _, p := range px {
		if int(p.A) < lo {
			lo = int(p.A)
		}
		if int(p.A) > hi {
			hi = int(p.A)
		}
	}
	qa := [2]int{quantise(lo, 6), quantise(hi, 6)}
	da := [2]int{dequant(qa[0], 6), dequant(qa[1], 6)}
	var aidx [16]int
	for i, p := range px {
		aidx[i] = nearestIndex3(da, int(p.A))
	}
	if aidx[0] > 3 {
		// Complementing a 3-bit index is exact: w[7-i] == 64-w[i].
		qa[0], qa[1] = qa[1], qa[0]
		da[0], da[1] = da[1], da[0]
		for i := range aidx {
			aidx[i] = 7 - aidx[i]
		}
	}

	var w bitWriter
	w.bits(0, 4)
	w.bit(1)     // mode 4
	w.bits(0, 2) // rotation
	w.bit(0)     // index mode: 2-bit colour, 3-bit alpha
	for c := 0; c < 3; c++ {
		for g := 0; g < 2; g++ {
			w.bits(qc[g][c], 5)
		}
	}
	w.bits(qa[0], 6)
	w.bits(qa[1], 6)
	w.bit(cidx[0] & 1) // fix-up: MSB implied zero
	for i := 1; i < 16; i++ {
		w.bits(cidx[i], 2)
	}
	w.bits(aidx[0], 2) // fix-up: MSB implied zero
	for i := 1; i < 16; i++ {
		w.bits(aidx[i], 3)
	}
	var out [16]byte
	copy(out[:], w.data)
	return out
}

// quantise maps an eight-bit value to an n-bit endpoint.
func quantise(v, n int) int {
	max := (1 << n) - 1
	return (v*max + 127) / 255
}

// nearestIndex3 returns the 3-bit index whose interpolated alpha is closest to v.
func nearestIndex3(e [2]int, v int) int {
	best, bestErr := 0, -1
	for i, w := range bc7Weights3 {
		d := v - lerp64(e[0], e[1], w)
		err := d * d
		if bestErr < 0 || err < bestErr {
			best, bestErr = i, err
		}
	}
	return best
}

// nearestColourIndex returns the 2-bit index whose interpolated RGB is closest
// to p for the given colour endpoints.
func nearestColourIndex(e [2][3]int, p pixel) int {
	best, bestErr := 0, -1
	for i, w := range bc7Weights2 {
		err := 0
		for c := 0; c < 3; c++ {
			d := channel(p, c) - lerp64(e[0][c], e[1][c], w)
			err += d * d
		}
		if bestErr < 0 || err < bestErr {
			best, bestErr = i, err
		}
	}
	return best
}

// nearestIndex4 returns the 4-bit index whose interpolated colour is closest to p.
func nearestIndex4(e [2][4]int, p pixel) int {
	best, bestErr := 0, -1
	for i, w := range bc7Weights4 {
		err := 0
		for c := 0; c < 4; c++ {
			d := channel(p, c) - lerp64(e[0][c], e[1][c], w)
			err += d * d
		}
		if bestErr < 0 || err < bestErr {
			best, bestErr = i, err
		}
	}
	return best
}

// fitEndpoints chooses two endpoints and 16 indices for arbitrary content by
// starting from the per-channel range and refining with a least-squares fit.
func fitEndpoints(px [16]pixel) ([2][4]int, [16]int) {
	var e [2][4]int
	for c := 0; c < 4; c++ {
		lo, hi := 255, 0
		for _, p := range px {
			v := channel(p, c)
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		e[0][c] = lo
		e[1][c] = hi
	}
	var idx [16]int
	for i, p := range px {
		idx[i] = nearestIndex4(e, p)
	}
	// Least-squares refinement per channel: minimise the squared error of
	// p = (1-w)*e0 + w*e1 over the chosen weights.
	for c := 0; c < 4; c++ {
		var saa, sab, sbb, sap, sbp float64
		for i, p := range px {
			w := float64(bc7Weights4[idx[i]]) / 64
			a := 1 - w
			b := w
			v := float64(channel(p, c))
			saa += a * a
			sab += a * b
			sbb += b * b
			sap += a * v
			sbp += b * v
		}
		det := saa*sbb - sab*sab
		if det < 1e-9 {
			continue
		}
		e0 := (sap*sbb - sbp*sab) / det
		e1 := (saa*sbp - sab*sap) / det
		e[0][c] = int(e0 + 0.5)
		e[1][c] = int(e1 + 0.5)
	}
	for i, p := range px {
		idx[i] = nearestIndex4(e, p)
	}
	return e, idx
}

func channel(p pixel, c int) int {
	switch c {
	case 0:
		return int(p.R)
	case 1:
		return int(p.G)
	case 2:
		return int(p.B)
	default:
		return int(p.A)
	}
}

func channelPtr(p *pixel, c int) *uint8 {
	switch c {
	case 0:
		return &p.R
	case 1:
		return &p.G
	default:
		return &p.B
	}
}

func clamp8(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
