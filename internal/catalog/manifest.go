package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const (
	DraftSchema  = "uo-unburger.translations.draft-1"
	Honorifics   = "honorifics"
	NoHonorifics = "no_honorifics"
)

var hexSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Record struct {
	ID             string  `json:"id"`
	SourceSHA256   string  `json:"source_sha256"`
	TargetEN       string  `json:"target_en"`
	NoHonorificsEN *string `json:"no_honorifics_en,omitempty"`
}

type Manifest struct {
	Schema           string   `json:"schema"`
	Locale           string   `json:"locale"`
	CanonicalVariant string   `json:"canonical_variant"`
	Records          []Record `json:"records"`
}

func (r Record) TextForVariant(variant string) (string, error) {
	switch variant {
	case Honorifics:
		return r.TargetEN, nil
	case NoHonorifics:
		if r.NoHonorificsEN != nil {
			return *r.NoHonorificsEN, nil
		}
		return r.TargetEN, nil
	default:
		return "", fmt.Errorf("unknown edition %q", variant)
	}
}

func ReadManifest(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}

	var remaining any
	if err := decoder.Decode(&remaining); err != io.EOF {
		if err == nil {
			return Manifest{}, errors.New("manifest contains trailing JSON")
		}
		return Manifest{}, fmt.Errorf("manifest contains trailing content: %w", err)
	}

	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.Schema != DraftSchema {
		return fmt.Errorf("unsupported manifest schema: %q", m.Schema)
	}
	if m.Locale != "en" || m.CanonicalVariant != Honorifics {
		return errors.New("manifest must declare English and honorifics as canonical")
	}
	if m.Records == nil {
		return errors.New("manifest records must be an array")
	}
	ids := make(map[string]struct{}, len(m.Records))
	for i, rec := range m.Records {
		if strings.TrimSpace(rec.ID) == "" {
			return fmt.Errorf("records[%d]: missing stable ID", i)
		}
		if _, exists := ids[rec.ID]; exists {
			return fmt.Errorf("records[%d]: duplicate ID %q", i, rec.ID)
		}
		ids[rec.ID] = struct{}{}
		if !hexSHA256.MatchString(rec.SourceSHA256) {
			return fmt.Errorf("records[%d]: expected lowercase 64-character source SHA-256", i)
		}
		if rec.TargetEN == "" {
			return fmt.Errorf("records[%d]: empty canonical target text", i)
		}
		if rec.NoHonorificsEN != nil {
			if *rec.NoHonorificsEN == "" {
				return fmt.Errorf("records[%d]: empty no-honorifics override", i)
			}
			if *rec.NoHonorificsEN == rec.TargetEN {
				return fmt.Errorf("records[%d]: redundant no-honorifics override", i)
			}
		}
	}
	return nil
}
