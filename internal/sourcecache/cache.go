package sourcecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"uo-unburger/internal/cpk"
)

// CacheSchema is the manifest schema version this package writes and reads.
const CacheSchema = 1

// CPKMemberName is the localization archive name inside a cache entry.
const CPKMemberName = "Unicorn_US.CPK"

// MainMemberName is the executable excerpt a voice patch would need.
const MainMemberName = "main"

// ManifestFileName is the cache manifest file name.
const ManifestFileName = "manifest.json"

const quarantineDirName = "quarantine"

// fingerprintChunk is how many leading/trailing bytes a quick source fingerprint
// covers.
const fingerprintChunk = 64 << 10

// Fingerprint is a cheap, reproducible identity for a game container: size,
// modification time and the SHA-256 of its leading and trailing bytes. A full
// digest is recorded separately when it has been computed.
type Fingerprint struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModUnix    int64  `json:"mod_unix"`
	HeadSHA256 string `json:"head_sha256"`
	TailSHA256 string `json:"tail_sha256"`
	FullSHA256 string `json:"full_sha256,omitempty"`
}

// Artifact describes a file stored inside a cache entry.
type Artifact struct {
	Name             string `json:"name"`
	Size             int64  `json:"size"`
	SHA256           string `json:"sha256"`
	Members          int    `json:"members,omitempty"`
	FMSMembers       int    `json:"fms_members,omitempty"`
	MemberListSHA256 string `json:"member_list_sha256,omitempty"`
	BuildID          string `json:"build_id,omitempty"`
}

// ExtractorInfo records which external tool produced an entry.
type ExtractorInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
}

// Manifest is the cache entry index.
type Manifest struct {
	Schema     int            `json:"schema"`
	TitleID    string         `json:"title_id"`
	RawVersion uint32         `json:"raw_version"`
	Role       string         `json:"role,omitempty"`
	MetaType   string         `json:"meta_type,omitempty"`
	Source     Fingerprint    `json:"source"`
	CPK        Artifact       `json:"cpk"`
	Main       *Artifact      `json:"main,omitempty"`
	Extractor  *ExtractorInfo `json:"extractor,omitempty"`
	CreatedUTC string         `json:"created_utc,omitempty"`
	Notes      []string       `json:"notes,omitempty"`
}

// EntryName is the cache entry directory name for a container.
func EntryName(titleID string, rawVersion uint32) string {
	return fmt.Sprintf("%s-%d", strings.ToUpper(strings.TrimSpace(titleID)), rawVersion)
}

// EntryDir is the absolute cache entry directory.
func EntryDir(cacheRoot, titleID string, rawVersion uint32) string {
	return filepath.Join(cacheRoot, EntryName(titleID, rawVersion))
}

// FingerprintFile computes the cheap container fingerprint.
func FingerprintFile(path string) (Fingerprint, error) {
	fp := Fingerprint{Path: path}
	f, err := os.Open(path)
	if err != nil {
		return fp, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fp, err
	}
	fp.Size = info.Size()
	fp.ModUnix = info.ModTime().Unix()
	head := make([]byte, fingerprintChunk)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return fp, err
	}
	fp.HeadSHA256 = sha256Hex(head[:n])
	if fp.Size > fingerprintChunk {
		tail := make([]byte, fingerprintChunk)
		n, err := f.ReadAt(tail, fp.Size-int64(fingerprintChunk))
		if err != nil && !errors.Is(err, io.EOF) {
			return fp, err
		}
		fp.TailSHA256 = sha256Hex(tail[:n])
	} else {
		fp.TailSHA256 = fp.HeadSHA256
	}
	return fp, nil
}

// FileSHA256 streams a full digest.
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// DescribeCPK computes the verifiable identity of a localization CPK: size,
// SHA-256, member counts and the member-list digest.
func DescribeCPK(path string) (Artifact, error) {
	var a Artifact
	a.Name = filepath.Base(path)
	sum, size, err := FileSHA256(path)
	if err != nil {
		return a, err
	}
	arch, err := cpk.OpenFile(path)
	if err != nil {
		return a, fmt.Errorf("sourcecache: %s is not a supported CPK: %w", path, err)
	}
	defer arch.Close()
	paths := make([]string, 0, len(arch.Entries))
	fms := 0
	for _, e := range arch.Entries {
		p := e.Path()
		paths = append(paths, p)
		if strings.HasSuffix(strings.ToLower(p), ".fms") {
			fms++
		}
	}
	a.Size = size
	a.SHA256 = sum
	a.Members = len(paths)
	a.FMSMembers = fms
	a.MemberListSHA256 = cpk.MemberListDigest(paths)
	return a, nil
}

// DescribeMain computes the executable excerpt identity, including the NSO Build
// ID (the module id at the NSO header offset 0x40) when the file is an NSO.
func DescribeMain(path string) (Artifact, error) {
	var a Artifact
	info, err := os.Stat(path)
	if err != nil {
		return a, err
	}
	a.Name = MainMemberName
	a.Size = info.Size()
	sum, size, err := FileSHA256(path)
	if err != nil {
		return a, err
	}
	a.SHA256 = sum
	a.Size = size
	if id, err := readNSOBuildID(path); err == nil {
		a.BuildID = id
	}
	return a, nil
}

// readNSOBuildID reads the module id at NSO header offset 0x40 (32 bytes) and
// returns the first 16 bytes as lower-case hex, or the non-zero prefix when the
// remainder is zero-padded. It never writes.
func readNSOBuildID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 32)
	if _, err := f.ReadAt(buf, 0x40); err != nil {
		return "", err
	}
	// Trim trailing zero bytes from the 32-byte field.
	end := len(buf)
	for end > 0 && buf[end-1] == 0 {
		end--
	}
	if end == 0 {
		return "", fmt.Errorf("sourcecache: %s has an empty Build ID field", path)
	}
	return hex.EncodeToString(buf[:end]), nil
}

// Verify checks that an artifact on disk matches its recorded identity. When a is
// a CPK the member list and member counts are re-derived, so a same-size but
// different archive is still rejected.
func Verify(path string, a Artifact) error {
	if a.Size == 0 && a.SHA256 == "" {
		return fmt.Errorf("sourcecache: empty expected identity for %s", a.Name)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if a.Size != 0 && info.Size() != a.Size {
		return fmt.Errorf("sourcecache: %s size %d, expected %d", a.Name, info.Size(), a.Size)
	}
	got, _, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if a.SHA256 != "" && !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("sourcecache: %s SHA-256 mismatch", a.Name)
	}
	if a.Members > 0 || a.MemberListSHA256 != "" {
		arch, err := cpk.OpenFile(path)
		if err != nil {
			return fmt.Errorf("sourcecache: %s no longer parses as a CPK: %w", a.Name, err)
		}
		defer arch.Close()
		paths := make([]string, 0, len(arch.Entries))
		for _, e := range arch.Entries {
			paths = append(paths, e.Path())
		}
		if a.Members > 0 && len(paths) != a.Members {
			return fmt.Errorf("sourcecache: %s member count %d, expected %d", a.Name, len(paths), a.Members)
		}
		if a.MemberListSHA256 != "" {
			if d := cpk.MemberListDigest(paths); !strings.EqualFold(d, a.MemberListSHA256) {
				return fmt.Errorf("sourcecache: %s member-list identity mismatch", a.Name)
			}
		}
	}
	if a.BuildID != "" {
		id, err := readNSOBuildID(path)
		if err != nil {
			return fmt.Errorf("sourcecache: %s Build ID unavailable: %w", a.Name, err)
		}
		if !strings.EqualFold(id, a.BuildID) {
			return fmt.Errorf("sourcecache: %s Build ID %s, expected %s", a.Name, id, a.BuildID)
		}
	}
	return nil
}

// ReadManifest loads a cache entry manifest.
func ReadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("sourcecache: parsing manifest: %w", err)
	}
	if m.Schema != CacheSchema {
		return nil, fmt.Errorf("sourcecache: unsupported manifest schema %d", m.Schema)
	}
	return &m, nil
}

// WriteManifest writes a manifest atomically.
func WriteManifest(dir string, m *Manifest) error {
	if m.Schema == 0 {
		m.Schema = CacheSchema
	}
	if m.CreatedUTC == "" {
		m.CreatedUTC = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, ManifestFileName))
}

// Lookup returns the manifest of an entry when one exists (without verifying its
// artifacts).
func Lookup(cacheRoot, titleID string, rawVersion uint32) (*Manifest, string, bool) {
	dir := EntryDir(cacheRoot, titleID, rawVersion)
	m, err := ReadManifest(dir)
	if err != nil {
		return nil, dir, false
	}
	return m, dir, true
}

// Reuse returns a cache entry for a container when it exists and both the source
// fingerprint and the artifact identities still match. When the entry is present
// but damaged it is quarantined (never deleted) and ok is false; err describes the
// damage so a caller can explain why a re-extraction is happening.
func Reuse(cacheRoot string, c Container, now time.Time) (m *Manifest, dir string, ok bool, err error) {
	if !c.Valid() {
		return nil, "", false, fmt.Errorf("sourcecache: container has no usable identity")
	}
	m, dir, found := Lookup(cacheRoot, c.TitleID, c.RawVersion)
	if !found {
		return nil, dir, false, nil
	}
	if reason := validateEntry(dir, m); reason != "" {
		if _, qerr := Quarantine(cacheRoot, dir, now); qerr != nil {
			return nil, dir, false, fmt.Errorf("%s; quarantining the entry also failed: %w", reason, qerr)
		}
		return nil, dir, false, errors.New(reason)
	}
	return m, dir, true, nil
}

// validateEntry returns "" when an entry is intact, else a human-readable reason.
func validateEntry(dir string, m *Manifest) string {
	if m.TitleID == "" || m.CPK.Name == "" {
		return "cache manifest is incomplete"
	}
	cpkPath := filepath.Join(dir, m.CPK.Name)
	if err := Verify(cpkPath, m.CPK); err != nil {
		return "cache localization archive failed verification: " + err.Error()
	}
	if m.Main != nil {
		mainPath := filepath.Join(dir, m.Main.Name)
		if err := Verify(mainPath, *m.Main); err != nil {
			return "cache executable excerpt failed verification: " + err.Error()
		}
	}
	return ""
}

// Check verifies a cache entry without modifying it (unlike Reuse, a damaged
// entry is not quarantined). It is used by read-only reporting.
func Check(dir string, m *Manifest) error {
	if reason := validateEntry(dir, m); reason != "" {
		return errors.New(reason)
	}
	return nil
}

// Quarantine renames a damaged entry aside under <cacheRoot>/quarantine/ and
// returns the new path. It never deletes anything.
func Quarantine(cacheRoot, dir string, now time.Time) (string, error) {
	base := filepath.Base(dir)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", fmt.Errorf("sourcecache: refusing to quarantine %q", dir)
	}
	qdir := filepath.Join(cacheRoot, quarantineDirName)
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(qdir, fmt.Sprintf("%s.%d", base, now.UTC().Unix()))
	for i := 1; ; i++ {
		if _, err := os.Lstat(dest); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dest = filepath.Join(qdir, fmt.Sprintf("%s.%d.%d", base, now.UTC().Unix(), i))
	}
	if err := os.Rename(dir, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// CommitRequest describes a freshly extracted artifact set.
type CommitRequest struct {
	CacheRoot string
	Container Container
	CPKPath   string
	// CPK carries any already-derived member identity for the archive (members,
	// .fms count, member-list digest). Commit always computes the size and
	// SHA-256 itself; when CPK.SHA256 is set it must match.
	CPK Artifact
	// MainPath describes the optional executable excerpt; Main carries any
	// already-derived identity.
	MainPath  string
	Main      *Artifact
	Extractor *ExtractorInfo
	Notes     []string
	Now       time.Time
}

// Commit copies an extracted artifact set into a new cache entry atomically. The
// entry only becomes visible after every artifact and the manifest are written,
// so an interrupted commit never leaves a partial entry that a later run could
// mistake for valid.
func Commit(req CommitRequest) (*Manifest, string, error) {
	if !req.Container.Valid() {
		return nil, "", fmt.Errorf("sourcecache: container has no usable identity")
	}
	if req.CPKPath == "" {
		return nil, "", fmt.Errorf("sourcecache: no localization archive to commit")
	}
	final := EntryDir(req.CacheRoot, req.Container.TitleID, req.Container.RawVersion)
	if _, err := os.Stat(final); err == nil {
		return nil, final, fmt.Errorf("sourcecache: cache entry %s already exists", EntryName(req.Container.TitleID, req.Container.RawVersion))
	}
	if err := os.MkdirAll(req.CacheRoot, 0o755); err != nil {
		return nil, final, err
	}
	tmp, err := os.MkdirTemp(req.CacheRoot, ".entry-*")
	if err != nil {
		return nil, final, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(tmp)
		}
	}()

	m := &Manifest{
		Schema:     CacheSchema,
		TitleID:    req.Container.TitleID,
		RawVersion: req.Container.RawVersion,
		Role:       string(req.Container.Role),
		MetaType:   req.Container.MetaType,
		Extractor:  req.Extractor,
		Notes:      req.Notes,
	}
	if fp, err := FingerprintFile(req.Container.Path); err == nil {
		m.Source = fp
	}

	cpkArt := req.CPK
	cpkArt.Name = CPKMemberName
	sum, size, err := FileSHA256(req.CPKPath)
	if err != nil {
		return nil, final, err
	}
	if cpkArt.SHA256 != "" && !strings.EqualFold(cpkArt.SHA256, sum) {
		return nil, final, fmt.Errorf("sourcecache: localization archive digest changed during extraction")
	}
	cpkArt.SHA256 = sum
	if cpkArt.Size == 0 {
		cpkArt.Size = size
	}
	if err := copyFile(req.CPKPath, filepath.Join(tmp, CPKMemberName)); err != nil {
		return nil, final, err
	}
	m.CPK = cpkArt

	if req.MainPath != "" {
		mainArt := Artifact{Name: MainMemberName}
		if req.Main != nil {
			mainArt = *req.Main
			mainArt.Name = MainMemberName
		}
		msum, msize, err := FileSHA256(req.MainPath)
		if err != nil {
			return nil, final, err
		}
		mainArt.SHA256 = msum
		mainArt.Size = msize
		if mainArt.BuildID == "" {
			if id, err := readNSOBuildID(req.MainPath); err == nil {
				mainArt.BuildID = id
			}
		}
		if err := copyFile(req.MainPath, filepath.Join(tmp, MainMemberName)); err != nil {
			return nil, final, err
		}
		m.Main = &mainArt
	}

	if !req.Now.IsZero() {
		m.CreatedUTC = req.Now.UTC().Format(time.RFC3339)
	}
	if err := WriteManifest(tmp, m); err != nil {
		return nil, final, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, final, err
	}
	cleanup = false
	return m, final, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
