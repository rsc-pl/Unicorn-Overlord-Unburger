package cpk

import "errors"

// Sentinel errors reported by the reader and writer.
var (
	errNotCRILAYLA       = errors.New("cpk: payload is not CRILAYLA-compressed")
	errCRILAYLAHeader    = errors.New("cpk: CRILAYLA header out of range")
	errCRILAYLATruncated = errors.New("cpk: CRILAYLA stream truncated")
	errCRILAYLAOverrun   = errors.New("cpk: CRILAYLA output overrun")
	errCRILAYLABackRef   = errors.New("cpk: CRILAYLA back-reference out of range")
)
