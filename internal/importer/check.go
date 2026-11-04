package importer

import "fmt"

// Check resolves the translation ledger against a reference JP/EN corpus,
// read-only. This is the advanced mode: both source_jp_sha and original_en_sha
// are re-derived from the corpus. It is the single shared implementation behind
// `import --check --corpus`, the builder's `--corpus` mode, the QA `--corpus`
// cross-check and the interactive interface's reference-data mode, so all front
// ends resolve records identically and cannot drift apart.
//
// A normal build does not need a corpus: it uses CheckAgainstSource instead, and
// a ledger-only structural check is available through CheckLedger.
func Check(dir, corpusPath string) (*Resolution, error) {
	if corpusPath == "" {
		return nil, fmt.Errorf("importer: corpus path is required")
	}
	corpus, err := LoadCorpusJSONL(corpusPath)
	if err != nil {
		return nil, err
	}
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		return nil, err
	}
	return cp.ResolveWith(ResolveOptions{
		Corpus:   corpus,
		VerifyJP: true,
		Official: true,
		Mode:     ModeReferenceCorpus,
	}), nil
}
