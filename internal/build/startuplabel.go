package build

import (
	"fmt"
	"sort"
)

// StartupLabelMember is the localization member that carries the game's
// startup/legal notice. On the western source it is MsgSheet/UcSysMsg.fms with
// 1462 slots.
const StartupLabelMember = "MsgSheet/UcSysMsg.fms"

// StartupLabelFile is the bare FMS member name (without the MsgSheet/ prefix).
const StartupLabelFile = "UcSysMsg.fms"

// StartupLabelSlot is the record replaced by the build-time startup label. On
// the western source it holds the fiction disclaimer ("This is a work of
// fiction. ..."); the whole slot is replaced.
const StartupLabelSlot = 1326

// applyStartupLabel forces the shared plan's startup record to the rendered
// version label, identically for both editions. It is a build-time override and
// is deliberately separate from the translation tables: the label is a
// property of the build (it tracks the root version.txt), not of the translation
// dataset, so it is never added to translations/*.tsv and it wins over any
// translation row that happens to target the same slot.
//
// An empty version yields no label and no edit (a build without a version, e.g.
// a dry run, simply carries none). The returned string is the applied label.
func applyStartupLabel(plan *Plan, version string) (string, error) {
	label := FormatTag(version)
	if label == "" {
		return "", nil
	}
	if plan == nil {
		return "", fmt.Errorf("build: nil plan")
	}
	fp := plan.Files[StartupLabelMember]
	if fp == nil {
		fp = &FilePlan{File: StartupLabelFile, Member: StartupLabelMember, Edits: map[int]*Edit{}}
		plan.Files[StartupLabelMember] = fp
		plan.Order = append(plan.Order, StartupLabelMember)
		sort.Strings(plan.Order)
	}
	// Both editions carry the same bytes, so the startup label can never drift
	// between honorifics and no-honorifics.
	raw := []byte(label)
	fp.Edits[StartupLabelSlot] = &Edit{
		RecordID:     fmt.Sprintf("%s:%d", StartupLabelFile, StartupLabelSlot),
		File:         StartupLabelFile,
		Member:       StartupLabelMember,
		Slot:         StartupLabelSlot,
		Honorifics:   raw,
		NoHonorifics: raw,
	}
	return label, nil
}
