package importer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// LoadCorpusJSONL streams a reference corpus file and returns the JP source and
// official Western English text keyed by record_id. Only the keys record_id,
// jp and en are used. Reading is line-by-line so memory stays bounded.
func LoadCorpusJSONL(path string) (map[string]CorpusRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type raw struct {
		RecordID string `json:"record_id"`
		JP       string `json:"jp"`
		EN       string `json:"en"`
	}

	out := map[string]CorpusRow{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r raw
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("corpus %s: %w", path, err)
		}
		if r.RecordID == "" {
			continue
		}
		out[r.RecordID] = CorpusRow{JP: r.JP, OfficialEN: r.EN}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("corpus %s: %w", path, err)
	}
	return out, nil
}
