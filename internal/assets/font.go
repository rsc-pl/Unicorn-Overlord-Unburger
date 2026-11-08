// Package assets holds small build-time assets that are compiled into the
// toolkit: the footer typeface and its authoritative typography configuration.
// No game data is stored here.
package assets

import _ "embed"

// FontTTF is the embedded footer typeface. Because //go:embed compiles the
// bytes into the binary, replacing font.ttf with another supported TrueType
// face requires rebuilding the Go binary; no runtime lookup is involved. The
// build report hashes these exact bytes, so a replacement changes the reported
// font identity without any hash allowlist.
//
//go:embed font.ttf
var FontTTF []byte

// TitleFooterJSON is the authoritative typography of the title-screen footer.
// Every normal build parses it; there are no silent in-code defaults for these
// values. See internal/titlefooter.LoadConfig for the schema.
//
//go:embed title_footer.json
var TitleFooterJSON []byte
