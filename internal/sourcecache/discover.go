package sourcecache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Format identifies a game container.
type Format string

const (
	// FormatNSP is a Nintendo Submission Package (PFS0).
	FormatNSP Format = "nsp"
	// FormatXCI is a game-card image.
	FormatXCI Format = "xci"
	// FormatUnknown is a container this package cannot classify.
	FormatUnknown Format = "unknown"
)

// Role is where a container sits in the update hierarchy.
type Role string

const (
	RoleBase   Role = "base"
	RoleUpdate Role = "update"
	RoleDLC    Role = "dlc"
	RoleOther  Role = "other"
)

// Container is the recognised metadata of one game container. All fields come
// from plaintext container metadata; a filename is used only as a non-authoritative
// display hint.
type Container struct {
	Path       string
	Format     Format
	Role       Role
	Size       int64
	TitleID    string // upper-case 16 hex digits
	RawVersion uint32
	MetaType   string // Application | Patch | AddOnContent
	OriginalID string // Patch: the TitleID it updates
	Members    int

	// DisplayHint is a human-readable version parsed from the filename (for
	// example "1.05" or "v262144"). It is never used for identity decisions.
	DisplayHint string

	// Parsed reports whether metadata could be read without an extractor.
	Parsed bool
	// Note explains why a container was not parsed.
	Note string
}

// DiscoverOptions configures discovery.
type DiscoverOptions struct {
	BaseDir   string
	UpdateDir string
	DLCDir    string
}

// Discovery is the result of scanning the game directories.
type Discovery struct {
	Containers []Container
	// Warnings lists files that were skipped or could not be parsed.
	Warnings []string
}

// DetectFormat classifies a container from its leading bytes. It reads at most
// 0x200 bytes and performs no decryption.
func DetectFormat(path string) Format {
	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown
	}
	defer f.Close()
	head := make([]byte, 0x200)
	n, _ := f.Read(head)
	head = head[:n]
	if len(head) >= 4 && bytes.Equal(head[0:4], []byte("PFS0")) {
		return FormatNSP
	}
	// A game-card image carries a "HEAD" header at 0x100.
	if len(head) >= 0x104 && bytes.Equal(head[0x100:0x104], []byte("HEAD")) {
		return FormatXCI
	}
	return FormatUnknown
}

// displayHint extracts the bracketed version tokens a filename commonly carries.
// It is presentational only.
func displayHint(name string) string {
	var parts []string
	for _, seg := range strings.Split(name, "]") {
		open := strings.LastIndex(seg, "[")
		if open < 0 {
			continue
		}
		tok := strings.TrimSpace(seg[open+1:])
		if tok == "" || strings.EqualFold(tok, "v0") {
			continue
		}
		parts = append(parts, tok)
	}
	return strings.Join(parts, " ")
}

// Discover scans the configured directories for NSP and XCI containers.
func Discover(opts DiscoverOptions) *Discovery {
	d := &Discovery{}
	scan := func(dir string, role Role) {
		if dir == "" {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				d.Warnings = append(d.Warnings, fmt.Sprintf("%s: %v", dir, err))
			}
			return
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			switch strings.ToLower(filepath.Ext(e.Name())) {
			case ".nsp", ".xci":
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(dir, name)
			c, err := Inspect(path, role)
			if err != nil {
				d.Warnings = append(d.Warnings, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			d.Containers = append(d.Containers, *c)
		}
	}
	scan(opts.BaseDir, RoleBase)
	scan(opts.UpdateDir, RoleUpdate)
	scan(opts.DLCDir, RoleDLC)
	return d
}

// Inspect recognises one container.
func Inspect(path string, role Role) (*Container, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("is a directory")
	}
	c := &Container{
		Path:        path,
		Role:        role,
		Size:        info.Size(),
		Format:      DetectFormat(path),
		DisplayHint: displayHint(filepath.Base(path)),
	}
	switch c.Format {
	case FormatNSP:
		cnmt, pfs, err := EmbeddedCNMT(path)
		if err != nil {
			c.Note = "NSP metadata could not be read: " + err.Error()
			return c, nil
		}
		c.TitleID = cnmt.TitleID
		c.RawVersion = cnmt.RawVersion
		c.MetaType = cnmt.Type
		c.OriginalID = cnmt.OriginalID
		c.Members = len(pfs.Members)
		c.Parsed = true
	case FormatXCI:
		c.Note = "XCI game-card images cannot be read without an extractor; recognition needs the external extractor path"
	default:
		return nil, fmt.Errorf("unrecognised container format")
	}
	return c, nil
}

// Find returns the first container whose TitleID matches id exactly.
func (d *Discovery) Find(id string) *Container {
	id = strings.ToUpper(strings.TrimSpace(id))
	for i := range d.Containers {
		if d.Containers[i].TitleID == id {
			return &d.Containers[i]
		}
	}
	return nil
}

// Pick selects the container that supplies the localization archive for a target
// base title. An update (Patch) whose OriginalID links to the target is preferred
// over the base application, mirroring the game's own layering.
func (d *Discovery) Pick(baseTitleID string) *Container {
	baseTitleID = strings.ToUpper(strings.TrimSpace(baseTitleID))
	var base *Container
	for i := range d.Containers {
		c := &d.Containers[i]
		if !c.Parsed {
			continue
		}
		if c.MetaType == "Patch" && (c.OriginalID == baseTitleID || c.TitleID == baseTitleID) {
			return c
		}
		if c.TitleID == baseTitleID {
			base = c
		}
	}
	return base
}

// Valid reports whether a container carries usable metadata.
func (c Container) Valid() bool { return c.Parsed && c.TitleID != "" }
