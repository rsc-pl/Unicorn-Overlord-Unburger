package importer

import (
	"encoding/csv"
	"fmt"
	"os"
)

// readTSV reads a tab-separated table. Fields may be double-quoted and may
// contain embedded tabs or newlines (RFC 4180 style), which the translation
// tables use for multi-line text. encoding/csv is used with a tab delimiter;
// variable field counts are permitted because the tables come in several
// documented shapes that are validated per file against their own header.
func readTSV(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.Comma = '\t'
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return rows, nil
}

// headerString renders a header row for error messages.
func headerString(hdr []string) string {
	out := ""
	for i, h := range hdr {
		if i > 0 {
			out += "\t"
		}
		out += h
	}
	return out
}
