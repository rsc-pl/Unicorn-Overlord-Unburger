package sourcecache

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// maxCNMTXML bounds the plaintext ContentMeta XML read from an NSP. The real
// files are a few KiB; the bound protects against a corrupt size field.
const maxCNMTXML = 1 << 20

// CNMT is the subset of a Nintendo Switch ContentMeta record this toolkit needs
// for recognition. It is read from the plaintext *.cnmt.xml member that every NSP
// produced by standard tooling embeds.
type CNMT struct {
	Type       string // Application | Patch | AddOnContent
	TitleID    string // upper-case 16 hex digits, no 0x
	RawVersion uint32
	OriginalID string // present for a Patch: the base TitleID it updates
	Contents   []CNMTContent
}

// CNMTContent is one declared content (NCA) of a ContentMeta record.
type CNMTContent struct {
	Type          string
	ID            string
	Size          uint64
	Hash          string
	KeyGeneration uint32
}

type cnmtXML struct {
	XMLName    xml.Name `xml:"ContentMeta"`
	Type       string   `xml:"Type"`
	ID         string   `xml:"Id"`
	Version    uint32   `xml:"Version"`
	OriginalID string   `xml:"OriginalId"`
	Contents   []struct {
		Type          string `xml:"Type"`
		ID            string `xml:"Id"`
		Size          uint64 `xml:"Size"`
		Hash          string `xml:"Hash"`
		KeyGeneration uint32 `xml:"KeyGeneration"`
	} `xml:"Content"`
}

// NormalizeTitleID upper-cases a 0x-prefixed or bare hexadecimal title ID and
// returns the 16-digit form. It rejects anything that is not 16 hex digits.
func NormalizeTitleID(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	s = strings.ToUpper(s)
	if len(s) != 16 {
		return "", fmt.Errorf("sourcecache: malformed title id %q", s)
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'F':
		default:
			return "", fmt.Errorf("sourcecache: malformed title id %q", s)
		}
	}
	return s, nil
}

// ParseCNMT parses the plaintext ContentMeta XML bytes.
func ParseCNMT(data []byte) (*CNMT, error) {
	var x cnmtXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("sourcecache: parsing ContentMeta XML: %w", err)
	}
	if strings.TrimSpace(x.Type) == "" {
		return nil, fmt.Errorf("sourcecache: ContentMeta XML has no Type")
	}
	id, err := NormalizeTitleID(x.ID)
	if err != nil {
		return nil, err
	}
	c := &CNMT{Type: x.Type, TitleID: id, RawVersion: x.Version}
	if x.OriginalID != "" {
		if orig, err := NormalizeTitleID(x.OriginalID); err == nil {
			c.OriginalID = orig
		}
	}
	for _, ct := range x.Contents {
		c.Contents = append(c.Contents, CNMTContent{
			Type:          ct.Type,
			ID:            strings.ToLower(strings.TrimSpace(ct.ID)),
			Size:          ct.Size,
			Hash:          strings.ToLower(strings.TrimSpace(ct.Hash)),
			KeyGeneration: ct.KeyGeneration,
		})
	}
	return c, nil
}

// EmbeddedCNMT reads and parses the plaintext *.cnmt.xml member of an NSP.
func EmbeddedCNMT(path string) (*CNMT, *PFS0, error) {
	p, err := OpenPFS0(path)
	if err != nil {
		return nil, nil, err
	}
	var member *PFS0Member
	for i := range p.Members {
		if strings.HasSuffix(strings.ToLower(p.Members[i].Name), ".cnmt.xml") {
			member = &p.Members[i]
			break
		}
	}
	if member == nil {
		return nil, p, fmt.Errorf("sourcecache: %s has no plaintext .cnmt.xml member", path)
	}
	if member.Size > maxCNMTXML {
		return nil, p, fmt.Errorf("sourcecache: %s: ContentMeta XML is implausibly large (%d bytes)", path, member.Size)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, p, err
	}
	defer f.Close()
	data, err := p.ReadMember(f, member, maxCNMTXML)
	if err != nil {
		return nil, p, err
	}
	c, err := ParseCNMT(data)
	if err != nil {
		return nil, p, err
	}
	return c, p, nil
}
