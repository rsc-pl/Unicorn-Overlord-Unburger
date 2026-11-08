package titlefooter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"uo-unburger/internal/assets"
)

// Config is the authoritative typography of the footer mark. Every value is in
// output pixels; font_size is pixels per em. There is no in-code default and no
// silent fallback: a missing or malformed configuration is a hard error.
type Config struct {
	// FontSize is the type size in output pixels per em.
	FontSize float64 `json:"font_size"`
	// OutlineSize is the dark outline's dilation radius in output pixels.
	OutlineSize float64 `json:"outline_size"`
	// LineSpacing is the extra vertical gap between the two line ink boxes in
	// output pixels.
	LineSpacing float64 `json:"line_spacing"`
	// OffsetX moves the whole mark horizontally in output pixels (+ right).
	OffsetX float64 `json:"offset_x"`
	// OffsetY moves the whole mark vertically in output pixels (+ down).
	OffsetY float64 `json:"offset_y"`
}

// ErrConfigInvalid reports a missing, malformed or out-of-range typography
// configuration.
var ErrConfigInvalid = errors.New("titlefooter: invalid typography configuration")

const (
	// maxFontSize bounds font_size so a typo cannot request an absurd raster.
	maxFontSize = 512.0
	// maxOutlineSize bounds outline_size; a radius larger than the strip can
	// never be represented.
	maxOutlineSize = float64(canvasHeight) / 2
	// maxLineSpacing bounds line_spacing to the strip height.
	maxLineSpacing = float64(canvasHeight)
	// maxOffset bounds |offset_x| and |offset_y|.
	maxOffset = float64(canvasWidth)
)

// configFile mirrors Config with pointer fields so a missing key can be told
// apart from an explicit zero.
type configFile struct {
	FontSize    *float64 `json:"font_size"`
	OutlineSize *float64 `json:"outline_size"`
	LineSpacing *float64 `json:"line_spacing"`
	OffsetX     *float64 `json:"offset_x"`
	OffsetY     *float64 `json:"offset_y"`
}

// LoadConfig parses and validates a typography configuration. The document must
// be a single JSON object with exactly the five required keys; unknown keys,
// trailing data, nulls and out-of-range values are rejected.
func LoadConfig(data []byte) (*Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty document", ErrConfigInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw configFile
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfigInvalid, err)
	}
	// Reject a second JSON value after the object.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: trailing content after the object", ErrConfigInvalid)
		}
		return nil, fmt.Errorf("%w: %v", ErrConfigInvalid, err)
	}

	var missing []string
	for name, p := range map[string]*float64{
		"font_size":    raw.FontSize,
		"outline_size": raw.OutlineSize,
		"line_spacing": raw.LineSpacing,
		"offset_x":     raw.OffsetX,
		"offset_y":     raw.OffsetY,
	} {
		if p == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: missing required key(s) %v", ErrConfigInvalid, missing)
	}
	cfg := &Config{
		FontSize:    *raw.FontSize,
		OutlineSize: *raw.OutlineSize,
		LineSpacing: *raw.LineSpacing,
		OffsetX:     *raw.OffsetX,
		OffsetY:     *raw.OffsetY,
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks that every value is finite and within the strip's representable
// range. It does not check that a particular text fits; layoutMark does that.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: nil configuration", ErrConfigInvalid)
	}
	finite := func(name string, v float64) error {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: %s is not finite", ErrConfigInvalid, name)
		}
		return nil
	}
	for name, v := range map[string]float64{
		"font_size": c.FontSize, "outline_size": c.OutlineSize,
		"line_spacing": c.LineSpacing, "offset_x": c.OffsetX, "offset_y": c.OffsetY,
	} {
		if err := finite(name, v); err != nil {
			return err
		}
	}
	if c.FontSize <= 0 || c.FontSize > maxFontSize {
		return fmt.Errorf("%w: font_size %.3f outside (0,%g]", ErrConfigInvalid, c.FontSize, maxFontSize)
	}
	if c.OutlineSize < 0 || c.OutlineSize > maxOutlineSize {
		return fmt.Errorf("%w: outline_size %.3f outside [0,%g]", ErrConfigInvalid, c.OutlineSize, maxOutlineSize)
	}
	if c.LineSpacing < 0 || c.LineSpacing > maxLineSpacing {
		return fmt.Errorf("%w: line_spacing %.3f outside [0,%g]", ErrConfigInvalid, c.LineSpacing, maxLineSpacing)
	}
	if math.Abs(c.OffsetX) > maxOffset || math.Abs(c.OffsetY) > maxOffset {
		return fmt.Errorf("%w: offset (%.3f,%.3f) exceeds %.0f px", ErrConfigInvalid, c.OffsetX, c.OffsetY, maxOffset)
	}
	return nil
}

var (
	configOnce sync.Once
	configData *Config
	configErr  error
)

// EmbeddedConfig returns the authoritative configuration compiled into the
// toolkit (internal/assets/title_footer.json). A malformed embedded document is
// an error on every call; there is no fallback.
func EmbeddedConfig() (*Config, error) {
	configOnce.Do(func() {
		configData, configErr = LoadConfig(assets.TitleFooterJSON)
	})
	if configErr != nil {
		return nil, configErr
	}
	c := *configData
	return &c, nil
}

// ConfigJSON returns a copy of the embedded configuration bytes. Build identity
// hashes this, so changing any typography value invalidates an otherwise
// identical run.
func ConfigJSON() []byte {
	return append([]byte(nil), assets.TitleFooterJSON...)
}
