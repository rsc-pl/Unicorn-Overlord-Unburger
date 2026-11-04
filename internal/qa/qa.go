// Package qa emits per-record static risk signals for the two translation
// editions. It is deliberately a static, non-rendering analysis: no signal it
// produces is a display or fit verification and DISPLAY_LAYOUT_UNVERIFIED
// remains in force. It reuses the revision-agnostic importer for resolution
// and the shared control-token lexical rules, so the signals stay consistent
// with the importer and builder.
//
// The command reads the translation ledger and a reference corpus read-only and
// never writes to either. Its only output is a deterministic text summary and a
// machine-readable TSV of per-record signals.
package qa

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"uo-unburger/internal/importer"
	"uo-unburger/internal/paths"
)

// Scope selects which edition(s) the signals describe.
type Scope string

const (
	ScopeBoth         Scope = "both"
	ScopeHonorifics   Scope = "honorifics"
	ScopeNoHonorifics Scope = "no-honorifics"
)

// ParseScope validates a --variant value.
func ParseScope(s string) (Scope, error) {
	switch Scope(s) {
	case ScopeBoth, ScopeHonorifics, ScopeNoHonorifics:
		return Scope(s), nil
	default:
		return "", fmt.Errorf("qa: unknown variant %q (want honorifics|no-honorifics|both)", s)
	}
}

// Options configures a QA run.
type Options struct {
	Scope           Scope
	CorpusPath      string
	SourcePath      string
	TranslationsDir string
}

// Static risk thresholds. They are project heuristics for comparing a
// translation with the official English, not measured renderer limits.
const (
	Ratio150        = 1.5
	Ratio200        = 2.0
	MaxLineGrowth   = 30
	OverLongVisible = 100
)

// RecordSignals is the static signal set for one record.
type RecordSignals struct {
	RecordID string
	Status   importer.Status

	OfficialVisible int
	OfficialLines   int
	OfficialMaxLine int

	CanonVisible   int
	CanonLines     int
	CanonMaxLine   int
	CanonRatio     float64
	CanonBreakLoss int
	CanonTokenFlag importer.TokenFlag
	CanonTokenDiff string

	NoHonVisible   int
	NoHonLines     int
	NoHonMaxLine   int
	NoHonRatio     float64
	NoHonBreakLoss int
	NoHonTokenFlag importer.TokenFlag
	NoHonTokenDiff string

	OverrideDivergence bool
	Flags              []string
}

// Counts summarises a QA run.
type Counts struct {
	Records            int
	Rejected           int
	TokenMismatch      int
	TokenAllowed       int
	BreakLoss          int
	LengthGE150        int
	LengthGE200        int
	MaxLineGrowth      int
	OverLongSingleLine int
	OverrideDivergence int
	TokenMismatchIDs   []string
}

// Result is the deterministic output of a QA run.
type Result struct {
	Records    []RecordSignals
	Counts     Counts
	SourceMode importer.SourceMode
	SummaryTxt string
	TSV        string
}

// Run loads the translation files and corpus, resolves both editions and computes the
// static signals. It performs no writes.
func Run(opts Options) (*Result, error) {
	if opts.Scope == "" {
		opts.Scope = ScopeBoth
	}
	if _, err := ParseScope(string(opts.Scope)); err != nil {
		return nil, err
	}

	var res *importer.Resolution
	var err error
	switch {
	case opts.CorpusPath != "":
		res, err = importer.Check(opts.TranslationsDir, opts.CorpusPath)
	case opts.SourcePath != "":
		res, err = importer.CheckAgainstSource(opts.TranslationsDir, opts.SourcePath)
	default:
		res, err = importer.CheckLedger(opts.TranslationsDir)
	}
	if err != nil {
		return nil, err
	}

	// Official English text is only available when the ledger was resolved
	// against a verified source archive or a reference corpus; without one the
	// signals that need official text are omitted rather than fabricated.
	officialAvailable := res.SourceMode != importer.ModeLedgerOnly

	out := &Result{SourceMode: res.SourceMode}
	for _, rr := range res.Records {
		official := rr.SourceEN
		canon := rr.Canonical
		if canon == "" {
			canon = official
		}
		nohon := rr.NoHonorifics
		if nohon == "" {
			nohon = canon
		}

		rs := RecordSignals{RecordID: rr.RecordID, Status: rr.Status}
		rs.OfficialVisible = importer.VisibleRunes(official)
		rs.OfficialLines = importer.AuthoredLines(official)
		rs.OfficialMaxLine = importer.MaxAuthoredSegment(official)

		flagSet := map[string]bool{}
		recordFlag := func(f string) { flagSet[f] = true }

		if !officialAvailable {
			recordFlag("SOURCE_IDENTITY_UNVERIFIED")
		}

		if opts.Scope != ScopeNoHonorifics {
			cv, cl, cm := variantSignals(canon)
			rs.CanonVisible, rs.CanonLines, rs.CanonMaxLine = cv, cl, cm
			if officialAvailable {
				r, bl, tf, td := officialVariantSignals(canon, official, rr.RecordID, nil)
				rs.CanonRatio, rs.CanonBreakLoss, rs.CanonTokenFlag, rs.CanonTokenDiff = r, bl, tf, td
			}
			recordFlags(flagSet, rs.OfficialVisible, rs.OfficialLines, rs.OfficialMaxLine, cv, cl, cm, rs.CanonRatio, rs.CanonBreakLoss, rs.CanonTokenFlag, officialAvailable)
		}
		if opts.Scope != ScopeHonorifics {
			nv, nl, nm := variantSignals(nohon)
			rs.NoHonVisible, rs.NoHonLines, rs.NoHonMaxLine = nv, nl, nm
			if officialAvailable {
				r, bl, tf, td := officialVariantSignals(nohon, official, rr.RecordID, nil)
				rs.NoHonRatio, rs.NoHonBreakLoss, rs.NoHonTokenFlag, rs.NoHonTokenDiff = r, bl, tf, td
			}
			recordFlags(flagSet, rs.OfficialVisible, rs.OfficialLines, rs.OfficialMaxLine, nv, nl, nm, rs.NoHonRatio, rs.NoHonBreakLoss, rs.NoHonTokenFlag, officialAvailable)
		}

		if opts.Scope == ScopeBoth && nohon != canon && rr.Status == importer.StatusApplied {
			rs.OverrideDivergence = true
			recordFlag("OVERRIDE_DIVERGENCE")
			out.Counts.OverrideDivergence++
		}
		if rs.CanonTokenFlag == importer.TokenMismatchF || rs.NoHonTokenFlag == importer.TokenMismatchF {
			out.Counts.TokenMismatch++
			out.Counts.TokenMismatchIDs = append(out.Counts.TokenMismatchIDs, rr.RecordID)
		}
		if rs.CanonTokenFlag == importer.TokenAllowed || rs.NoHonTokenFlag == importer.TokenAllowed {
			out.Counts.TokenAllowed++
		}

		if flagSet["AUTHORED_BREAK_LOSS"] {
			out.Counts.BreakLoss++
		}
		if flagSet["LENGTH_GE_150"] {
			out.Counts.LengthGE150++
		}
		if flagSet["LENGTH_GE_200"] {
			out.Counts.LengthGE200++
		}
		if flagSet["MAX_LINE_GROWTH_GE_30"] {
			out.Counts.MaxLineGrowth++
		}
		if flagSet["OVER_LONG_SINGLE_LINE_100"] {
			out.Counts.OverLongSingleLine++
		}

		rs.Flags = sortedKeys(flagSet)
		if rr.Status == importer.StatusRejected {
			out.Counts.Rejected++
		}
		out.Records = append(out.Records, rs)
	}

	sort.Slice(out.Records, func(i, j int) bool { return out.Records[i].RecordID < out.Records[j].RecordID })
	sort.Strings(out.Counts.TokenMismatchIDs)
	out.Counts.Records = len(out.Records)

	out.TSV = renderTSV(out, opts.Scope)
	out.SummaryTxt = renderSummary(out, opts)
	return out, nil
}

// variantSignals computes the signals that need no official English text.
func variantSignals(variant string) (visible, lines, maxLine int) {
	visible = importer.VisibleRunes(variant)
	lines = importer.AuthoredLines(variant)
	maxLine = importer.MaxAuthoredSegment(variant)
	return
}

// officialVariantSignals completes the signals that require official English
// text. When no official text is available it is a no-op.
func officialVariantSignals(variant, official, id string, exceptions map[string]bool) (
	ratio float64, breakLoss int, flag importer.TokenFlag, diffStr string) {
	if official == "" {
		return
	}
	visible := importer.VisibleRunes(variant)
	if ov := importer.VisibleRunes(official); ov > 0 {
		ratio = float64(visible) / float64(ov)
	}
	if il := importer.AuthoredLines(official); il > 1 {
		if lines := importer.AuthoredLines(variant); lines < il {
			breakLoss = il - lines
		}
	}
	diff := importer.TokenDiff(importer.Tokenize(variant), importer.Tokenize(official))
	if len(diff) > 0 {
		if exceptions[id] && importer.ItalicOnlyDiff(diff) {
			flag = importer.TokenAllowed
		} else {
			flag = importer.TokenMismatchF
		}
		diffStr = diffString(diff)
	}
	return
}

// recordFlags folds one edition's signals into the record flag set. Signals
// that need official English text are only folded in when it was available.
func recordFlags(flags map[string]bool, ov, ol, om, vv, vl, vm int,
	ratio float64, breakLoss int, flag importer.TokenFlag, officialAvailable bool) {
	if !officialAvailable {
		return
	}
	if breakLoss > 0 {
		flags["AUTHORED_BREAK_LOSS"] = true
	}
	if ratio >= Ratio200 {
		flags["LENGTH_GE_200"] = true
	}
	if ratio >= Ratio150 {
		flags["LENGTH_GE_150"] = true
	}
	if vm >= om+MaxLineGrowth && om > 0 {
		flags["MAX_LINE_GROWTH_GE_30"] = true
	}
	if vm > OverLongVisible {
		flags["OVER_LONG_SINGLE_LINE_100"] = true
	}
	switch flag {
	case importer.TokenMismatchF:
		flags["TOKEN_MISMATCH"] = true
	case importer.TokenAllowed:
		flags["TOKEN_ALLOWED"] = true
	}
}

func diffString(diff map[string]int) string {
	keys := make([]string, 0, len(diff))
	for k := range diff {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Itoa(diff[k]))
	}
	return strings.Join(parts, ";")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tsvHeader is the exact machine TSV column order.
var tsvHeader = []string{
	"record_id", "status", "variant_scope",
	"official_en_visible", "official_en_lines", "official_en_max_line",
	"canonical_visible", "canonical_lines", "canonical_max_line",
	"canonical_ratio", "canonical_break_loss", "canonical_token_flag", "canonical_token_diff",
	"nohon_visible", "nohon_lines", "nohon_max_line",
	"nohon_ratio", "nohon_break_loss", "nohon_token_flag", "nohon_token_diff",
	"override_divergence", "flags",
}

func renderTSV(res *Result, scope Scope) string {
	var b strings.Builder
	b.WriteString(strings.Join(tsvHeader, "\t"))
	b.WriteByte('\n')
	for _, r := range res.Records {
		cols := []string{
			r.RecordID, string(r.Status), string(scope),
			strconv.Itoa(r.OfficialVisible), strconv.Itoa(r.OfficialLines), strconv.Itoa(r.OfficialMaxLine),
		}
		if scope == ScopeNoHonorifics {
			cols = append(cols, "", "", "", "", "", "", "")
		} else {
			cols = append(cols,
				strconv.Itoa(r.CanonVisible), strconv.Itoa(r.CanonLines), strconv.Itoa(r.CanonMaxLine),
				formatRatio(r.CanonRatio), strconv.Itoa(r.CanonBreakLoss),
				tokenWord(r.CanonTokenFlag), r.CanonTokenDiff)
		}
		if scope == ScopeHonorifics {
			cols = append(cols, "", "", "", "", "", "", "")
		} else {
			cols = append(cols,
				strconv.Itoa(r.NoHonVisible), strconv.Itoa(r.NoHonLines), strconv.Itoa(r.NoHonMaxLine),
				formatRatio(r.NoHonRatio), strconv.Itoa(r.NoHonBreakLoss),
				tokenWord(r.NoHonTokenFlag), r.NoHonTokenDiff)
		}
		cols = append(cols, boolWord(r.OverrideDivergence), strings.Join(r.Flags, ";"))
		b.WriteString(strings.Join(cols, "\t"))
		b.WriteByte('\n')
	}
	return b.String()
}

func formatRatio(r float64) string {
	return strconv.FormatFloat(r, 'f', 3, 64)
}

func tokenWord(f importer.TokenFlag) string {
	if f == "" {
		return "OK"
	}
	return string(f)
}

func boolWord(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

func renderSummary(res *Result, opts Options) string {
	c := res.Counts
	var b strings.Builder
	fmt.Fprintf(&b, "uo-unburger qa (static signals only — NOT a fit/render check)\n")
	fmt.Fprintf(&b, "  variant:                    %s\n", opts.Scope)
	fmt.Fprintf(&b, "  translations:               %s\n", paths.Display(opts.TranslationsDir))
	if opts.CorpusPath != "" {
		fmt.Fprintf(&b, "  reference corpus:           %s\n", paths.Display(opts.CorpusPath))
	}
	if opts.SourcePath != "" {
		fmt.Fprintf(&b, "  source archive:             %s\n", paths.Display(opts.SourcePath))
	}
	fmt.Fprintf(&b, "  source identity:            %s\n", qaSourceWord(res.SourceMode))
	fmt.Fprintf(&b, "  records:                    %d\n", c.Records)
	fmt.Fprintf(&b, "  rejected:                   %d\n", c.Rejected)
	fmt.Fprintf(&b, "  token mismatch:             %d\n", c.TokenMismatch)
	fmt.Fprintf(&b, "  token allowed_with_exception: %d\n", c.TokenAllowed)
	fmt.Fprintf(&b, "  authored break loss:        %d\n", c.BreakLoss)
	fmt.Fprintf(&b, "  length ratio >= 150%%:        %d\n", c.LengthGE150)
	fmt.Fprintf(&b, "  length ratio >= 200%%:        %d\n", c.LengthGE200)
	fmt.Fprintf(&b, "  max authored line growth >= 30: %d\n", c.MaxLineGrowth)
	fmt.Fprintf(&b, "  over-long single line > 100: %d\n", c.OverLongSingleLine)
	fmt.Fprintf(&b, "  override divergence:        %d\n", c.OverrideDivergence)
	if len(c.TokenMismatchIDs) > 0 {
		fmt.Fprintf(&b, "  token mismatch ids:         %s\n", strings.Join(c.TokenMismatchIDs, " "))
	}
	fmt.Fprintf(&b, "  font metrics / auto-wrap / per-widget geometry: UNKNOWN\n")
	fmt.Fprintf(&b, "  DISPLAY_LAYOUT_UNVERIFIED:  in force (no reflow, no wording change)\n")
	if res.SourceMode == importer.ModeLedgerOnly {
		fmt.Fprintf(&b, "  NOTE: no source archive or corpus was supplied; length/token signals that need official English were omitted.\n")
	}
	return b.String()
}

// qaSourceWord renders a source provenance for a user.
func qaSourceWord(m importer.SourceMode) string {
	switch m {
	case importer.ModeVerifiedSourceCPK:
		return "verified against the source CPK"
	case importer.ModeReferenceCorpus:
		return "verified against the reference JP/EN corpus"
	default:
		return "records and linkage only (no source archive supplied)"
	}
}
