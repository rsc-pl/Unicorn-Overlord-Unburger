package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"uo-unburger/internal/importer"
)

// translationFilesStatus is a read-only summary of the public translation tables
// for the selected translation directory. It is the status the normal screens
// show instead of a reference-data requirement: the private JP/EN corpus is an
// advanced cross-check, never needed to load, validate or build.
//
// It is cached on the model and only re-read when the directory or one of the two
// files changes, so a table is never parsed twice for the same bytes.
type translationFilesStatus struct {
	key string

	dir          string
	honorifics   string
	noHonorifics string

	honorificsPresent bool
	noPresent         bool

	canonicalRows int
	overrideRows  int

	loaded bool
	err    string
}

// translationFiles resolves the public translation tables and (re-)loads them
// when the directory or either file changed. It never reads advanced reference
// data and never writes anything.
func (m *Model) translationFiles() translationFilesStatus {
	dir := choose(m.trans.form.Value("translations"), m.cfg.TranslationsDir)
	if strings.TrimSpace(dir) == "" {
		dir = "translations"
	}
	hon := filepath.Join(dir, importer.HonorificsFile)
	no := filepath.Join(dir, importer.NoHonorificsFile)
	key := dir + "|" + statSignature(hon) + "|" + statSignature(no)
	if m.trans.files.key == key {
		return m.trans.files
	}

	st := translationFilesStatus{key: key, dir: dir, honorifics: hon, noHonorifics: no}
	st.honorificsPresent = regularFile(hon)
	st.noPresent = regularFile(no)
	if cp, err := importer.LoadCheckpoint(dir); err != nil {
		st.err = err.Error()
	} else {
		st.loaded = true
		st.canonicalRows = len(cp.Canonical)
		st.overrideRows = len(cp.Overrides)
	}
	m.trans.files = st
	return st
}

// corpusPath resolves the optional advanced reference corpus. It is empty by
// default and is never required by a normal build, check or source acquisition.
func (m *Model) corpusPath() string {
	if m.diag.form != nil {
		if c := m.diag.form.Value("corpus"); c != "" {
			return c
		}
	}
	return m.cfg.Corpus
}

// statSignature is a cheap change detector for one file: size and modification
// time, or a fixed word when it is absent.
func statSignature(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// plural renders a count with a singular or plural noun ("1 record", "2 records").
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// honorificsWord is the status word for the canonical table. It starts with the
// word so the shared status styler can colour it.
func (st translationFilesStatus) honorificsWord() string {
	switch {
	case st.loaded && st.honorificsPresent:
		return fmt.Sprintf("loaded %d", st.canonicalRows)
	case !st.honorificsPresent:
		return "missing"
	default:
		return "invalid"
	}
}

// noHonorificsWord is the status word for the sparse override table. The table is
// optional, so an absent file is reported as "none", never as a blocker.
func (st translationFilesStatus) noHonorificsWord() string {
	switch {
	case st.loaded && st.noPresent:
		return fmt.Sprintf("loaded %d", st.overrideRows)
	case !st.noPresent:
		return "none"
	default:
		return "invalid"
	}
}

// honorificsNote and noHonorificsNote are the plain-language notes for the
// Build/Packages option tables.
func (st translationFilesStatus) honorificsNote() string {
	switch {
	case st.loaded && st.honorificsPresent:
		return fmt.Sprintf("%d canonical records validated", st.canonicalRows)
	case !st.honorificsPresent:
		return "not found — the translation folder is missing the canonical table"
	default:
		return "unreadable: " + truncate(st.err, 60)
	}
}

func (st translationFilesStatus) noHonorificsNote() string {
	switch {
	case st.loaded && st.noPresent:
		return fmt.Sprintf("%d sparse overrides validated", st.overrideRows)
	case !st.noPresent:
		return "none (optional sparse override table)"
	default:
		return "unreadable: " + truncate(st.err, 60)
	}
}
