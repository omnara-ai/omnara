// Package skills parses and extracts Agent Skills archives.
package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	MaxArchiveBytes       = 25 * 1024 * 1024
	MaxSkillMdBytes       = 256 * 1024
	MaxExtractedBytes     = 4 * MaxArchiveBytes
	MaxExtractedFileBytes = MaxArchiveBytes
	MaxArchiveEntries     = 1000

	MaxSkillNameChars = 64
	// MaxSkillDescriptionChars bounds the frontmatter description. Skill
	// descriptions are folded into the model's system-prompt catalog, so
	// without this cap one skill could contribute nearly the full SKILL.md
	// allowance to every prompt.
	MaxSkillDescriptionChars = 1024
)

// skillNamePattern is the Agent Skills name grammar: lowercase
// alphanumeric segments separated by single hyphens.
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func ValidateName(name string) error {
	if name == "" {
		return errors.New("is required")
	}
	if len(name) > MaxSkillNameChars {
		return fmt.Errorf("cannot exceed %d characters", MaxSkillNameChars)
	}
	if !skillNamePattern.MatchString(name) {
		return errors.New("must use lowercase alphanumeric segments separated by single hyphens")
	}
	return nil
}

var errExtractionLimitExceeded = errors.New("skill archive extraction limit exceeded")

type extractionBudget struct {
	totalRemaining int64
	entries        int
}

func newExtractionBudget() *extractionBudget {
	return &extractionBudget{totalRemaining: MaxExtractedBytes}
}

func (b *extractionBudget) accountEntry(name string) error {
	b.entries++
	if b.entries > MaxArchiveEntries {
		return fmt.Errorf("%w: archive has more than %d entries (at %q)", errExtractionLimitExceeded, MaxArchiveEntries, name)
	}
	return nil
}

func (b *extractionBudget) copyBounded(dst io.Writer, src io.Reader, name string) (int64, error) {
	limit := MaxExtractedFileBytes
	if int64(limit) > b.totalRemaining {
		limit = int(b.totalRemaining)
	}
	written, err := io.Copy(dst, io.LimitReader(src, int64(limit)+1))
	if err != nil {
		return written, err
	}
	if written > int64(limit) {
		if int64(limit) < int64(MaxExtractedFileBytes) {
			return written, fmt.Errorf(
				"%w: archive entries together exceed %d bytes (at %q)",
				errExtractionLimitExceeded,
				MaxExtractedBytes,
				name,
			)
		}
		return written, fmt.Errorf("%w: entry %q exceeds %d bytes", errExtractionLimitExceeded, name, MaxExtractedFileBytes)
	}
	b.totalRemaining -= written
	return written, nil
}

func (b *extractionBudget) accountFileSize(name string, size int64) error {
	if size < 0 {
		return fmt.Errorf("%w: entry %q has an invalid size", errExtractionLimitExceeded, name)
	}
	if size > int64(MaxExtractedFileBytes) {
		return fmt.Errorf("%w: entry %q exceeds %d bytes", errExtractionLimitExceeded, name, MaxExtractedFileBytes)
	}
	if size > b.totalRemaining {
		return fmt.Errorf(
			"%w: archive entries together exceed %d bytes (at %q)",
			errExtractionLimitExceeded,
			MaxExtractedBytes,
			name,
		)
	}
	b.totalRemaining -= size
	return nil
}

type ArchiveFormat string

const (
	FormatZip   ArchiveFormat = "zip"
	FormatTarGz ArchiveFormat = "tar.gz"
)

type Metadata struct {
	Format      ArchiveFormat
	Name        string
	Description string
	SkillMd     string
	RootDir     string
}

func ExtractMetadata(format ArchiveFormat, raw []byte) (Metadata, error) {
	if len(raw) == 0 {
		return Metadata{}, errors.New("skill archive is empty")
	}
	if len(raw) > MaxArchiveBytes {
		return Metadata{}, fmt.Errorf("skill archive exceeds %d bytes", MaxArchiveBytes)
	}
	entries, err := readEntries(format, raw)
	if err != nil {
		return Metadata{}, err
	}
	return validateAndExtract(format, entries)
}

func DetectFormat(filename string, raw []byte) (ArchiveFormat, bool) {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return FormatZip, true
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return FormatTarGz, true
	}
	if len(raw) >= 4 && raw[0] == 'P' && raw[1] == 'K' && (raw[2] == 0x03 || raw[2] == 0x05 || raw[2] == 0x07) {
		return FormatZip, true
	}
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		return FormatTarGz, true
	}
	return "", false
}

func VerifyDigest(content []byte, expected string) error {
	if !strings.HasPrefix(expected, "sha256:") {
		return fmt.Errorf("unsupported skill digest %q", expected)
	}
	sum := sha256.Sum256(content)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if got != expected {
		return fmt.Errorf("skill archive digest mismatch: got %s want %s", got, expected)
	}
	return nil
}

// ExtractInto unpacks the archive into dst.
func ExtractInto(format ArchiveFormat, raw []byte, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clear skill destination: %w", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create skill destination: %w", err)
	}
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("resolve skill destination: %w", err)
	}
	switch format {
	case FormatZip:
		return extractZip(raw, absDst)
	case FormatTarGz:
		return extractTarGz(raw, absDst)
	default:
		return fmt.Errorf("unsupported skill archive format %q", format)
	}
}

type FileEntry struct {
	Path string
	Size int64
}

func ListFiles(format ArchiveFormat, raw []byte) ([]FileEntry, error) {
	entries, err := readEntries(format, raw)
	if err != nil {
		return nil, err
	}
	files := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir || entry.IsLink {
			continue
		}
		_, rel, found := strings.Cut(entry.Path, "/")
		if !found || rel == "" {
			continue
		}
		files = append(files, FileEntry{Path: rel, Size: entry.Size})
	}
	slices.SortFunc(files, func(a, b FileEntry) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

func ReplaceSkillMd(format ArchiveFormat, raw []byte, skillMd string) ([]byte, error) {
	if len(skillMd) > MaxSkillMdBytes {
		return nil, fmt.Errorf("SKILL.md exceeds %d bytes", MaxSkillMdBytes)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	budget := newExtractionBudget()
	replaced := false
	writeEntry := func(entryPath string, content io.Reader) error {
		target, err := zw.Create(entryPath)
		if err != nil {
			return fmt.Errorf("create archive entry %q: %w", entryPath, err)
		}
		if isTopLevelSkillMd(entryPath) {
			if replaced {
				return errors.New("skill archive contains multiple SKILL.md files")
			}
			replaced = true
			if _, err := io.WriteString(target, skillMd); err != nil {
				return fmt.Errorf("write archive entry %q: %w", entryPath, err)
			}
			return nil
		}
		if _, err := budget.copyBounded(target, content, entryPath); err != nil {
			return fmt.Errorf("write archive entry %q: %w", entryPath, err)
		}
		return nil
	}
	switch format {
	case FormatZip:
		reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return nil, fmt.Errorf("read zip archive: %w", err)
		}
		for _, file := range reader.File {
			cleaned, err := cleanEntryPath(file.Name)
			if err != nil {
				return nil, err
			}
			if err := budget.accountEntry(cleaned); err != nil {
				return nil, err
			}
			if file.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("skill archive contains a symlink at %q", cleaned)
			}
			if strings.HasSuffix(file.Name, "/") || file.FileInfo().IsDir() {
				continue
			}
			body, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("open archive entry %q: %w", cleaned, err)
			}
			writeErr := writeEntry(cleaned, body)
			_ = body.Close()
			if writeErr != nil {
				return nil, writeErr
			}
		}
	case FormatTarGz:
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("open gzip stream: %w", err)
		}
		defer func() { _ = gz.Close() }()
		tr := tar.NewReader(gz)
		for {
			header, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("read tar archive: %w", err)
			}
			cleaned, err := cleanEntryPath(header.Name)
			if err != nil {
				return nil, err
			}
			if err := budget.accountEntry(cleaned); err != nil {
				return nil, err
			}
			switch header.Typeflag {
			case tar.TypeDir:
			case tar.TypeSymlink, tar.TypeLink:
				return nil, fmt.Errorf("skill archive contains a symlink at %q", cleaned)
			case tar.TypeReg:
				if err := writeEntry(cleaned, tr); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("unsupported tar entry type %q for %q", string(header.Typeflag), header.Name)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported skill archive format %q", format)
	}
	if !replaced {
		return nil, errors.New("skill archive is missing SKILL.md at the top-level directory")
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip archive: %w", err)
	}
	return out.Bytes(), nil
}

type archiveEntry struct {
	Path    string
	IsDir   bool
	IsLink  bool
	Size    int64
	Content []byte
}

func readEntries(format ArchiveFormat, raw []byte) ([]archiveEntry, error) {
	switch format {
	case FormatZip:
		return readZipEntries(raw)
	case FormatTarGz:
		return readTarGzEntries(raw)
	default:
		return nil, fmt.Errorf("unsupported skill archive format %q", format)
	}
}

func readZipEntries(raw []byte) ([]archiveEntry, error) {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("read zip archive: %w", err)
	}
	entries := make([]archiveEntry, 0, len(reader.File))
	for _, file := range reader.File {
		cleaned, err := cleanEntryPath(file.Name)
		if err != nil {
			return nil, err
		}
		entry := archiveEntry{Path: cleaned}
		switch {
		case file.Mode()&os.ModeSymlink != 0:
			entry.IsLink = true
		case strings.HasSuffix(file.Name, "/"), file.FileInfo().IsDir():
			entry.IsDir = true
		default:
			if file.UncompressedSize64 > uint64(MaxExtractedBytes)+1 {
				entry.Size = int64(MaxExtractedBytes) + 1
			} else {
				entry.Size = int64(file.UncompressedSize64)
			}
		}
		if !entry.IsDir && !entry.IsLink && isTopLevelSkillMd(cleaned) {
			rc, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("open archive entry %q: %w", file.Name, err)
			}
			body, readErr := io.ReadAll(io.LimitReader(rc, MaxSkillMdBytes+1))
			_ = rc.Close()
			if readErr != nil {
				return nil, fmt.Errorf("read archive entry %q: %w", file.Name, readErr)
			}
			entry.Content = body
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func isTopLevelSkillMd(cleaned string) bool {
	segments := strings.Split(cleaned, "/")
	return len(segments) == 2 && strings.EqualFold(segments[1], "SKILL.md")
}

func readTarGzEntries(raw []byte) ([]archiveEntry, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	// Budgets are enforced while walking the stream, not only in
	// validateAndExtract afterwards: advancing past a regular file with
	// tr.Next() decompresses its whole payload, so a bomb must be rejected
	// from the declared header before the walk moves beyond it.
	budget := newExtractionBudget()
	var entries []archiveEntry
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar archive: %w", err)
		}
		cleaned, err := cleanEntryPath(header.Name)
		if err != nil {
			return nil, err
		}
		if err := budget.accountEntry(cleaned); err != nil {
			return nil, err
		}
		entry := archiveEntry{Path: cleaned}
		switch header.Typeflag {
		case tar.TypeDir:
			entry.IsDir = true
		case tar.TypeSymlink, tar.TypeLink:
			entry.IsLink = true
		case tar.TypeReg:
			entry.Size = header.Size
			if err := budget.accountFileSize(cleaned, header.Size); err != nil {
				return nil, err
			}
			if isTopLevelSkillMd(cleaned) {
				body, readErr := io.ReadAll(io.LimitReader(tr, MaxSkillMdBytes+1))
				if readErr != nil {
					return nil, fmt.Errorf("read tar entry %q: %w", header.Name, readErr)
				}
				entry.Content = body
			}
		default:
			return nil, fmt.Errorf("unsupported tar entry type %q for %q", string(header.Typeflag), header.Name)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func cleanEntryPath(raw string) (string, error) {
	if strings.ContainsRune(raw, '\\') {
		return "", fmt.Errorf("archive entry %q uses backslash separators", raw)
	}
	trimmed := strings.TrimPrefix(raw, "./")
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("archive entry %q must be relative", raw)
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("archive entry %q is empty after cleaning", raw)
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return "", fmt.Errorf("archive entry %q escapes top-level directory", raw)
		}
	}
	return cleaned, nil
}

func validateAndExtract(format ArchiveFormat, entries []archiveEntry) (Metadata, error) {
	if len(entries) == 0 {
		return Metadata{}, errors.New("skill archive is empty")
	}
	budget := newExtractionBudget()
	var (
		rootDir    string
		skillMd    string
		hasSkillMd bool
	)
	for _, entry := range entries {
		if err := budget.accountEntry(entry.Path); err != nil {
			return Metadata{}, err
		}
		if entry.IsLink {
			return Metadata{}, fmt.Errorf("skill archive entry %q is a symlink; symlinks are not allowed", entry.Path)
		}
		segments := strings.Split(entry.Path, "/")
		if len(segments) == 0 || segments[0] == "" {
			return Metadata{}, fmt.Errorf("skill archive entry %q has no top-level directory", entry.Path)
		}
		if rootDir == "" {
			rootDir = segments[0]
		} else if segments[0] != rootDir {
			return Metadata{}, fmt.Errorf(
				"skill archive must contain exactly one top-level directory; got %q and %q",
				rootDir,
				segments[0],
			)
		}
		if entry.IsDir {
			continue
		}
		if err := budget.accountFileSize(entry.Path, entry.Size); err != nil {
			return Metadata{}, err
		}
		if len(segments) == 2 && strings.EqualFold(segments[1], "SKILL.md") {
			if hasSkillMd {
				return Metadata{}, errors.New("skill archive contains multiple SKILL.md files")
			}
			if len(entry.Content) > MaxSkillMdBytes {
				return Metadata{}, fmt.Errorf("SKILL.md exceeds %d bytes", MaxSkillMdBytes)
			}
			skillMd = string(entry.Content)
			hasSkillMd = true
		}
	}
	if rootDir == "" {
		return Metadata{}, errors.New("skill archive has no top-level directory")
	}
	if !hasSkillMd {
		return Metadata{}, errors.New("skill archive is missing SKILL.md at the top-level directory")
	}
	name, description, err := parseSkillMdFrontmatter(skillMd)
	if err != nil {
		return Metadata{}, err
	}
	if name != rootDir {
		return Metadata{}, fmt.Errorf(
			"SKILL.md frontmatter `name` %q must match the archive's top-level directory %q",
			name,
			rootDir,
		)
	}
	return Metadata{
		Format:      format,
		Name:        name,
		Description: description,
		SkillMd:     skillMd,
		RootDir:     rootDir,
	}, nil
}

func parseSkillMdFrontmatter(body string) (string, string, error) {
	frontmatter, ok := extractFrontmatterBlock(body)
	if !ok {
		return "", "", errors.New("SKILL.md is missing YAML frontmatter delimited by '---'")
	}
	type fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	var parsed fm
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		repaired, repairOK := repairUnquotedFrontmatter(frontmatter)
		if !repairOK || yaml.Unmarshal([]byte(repaired), &parsed) != nil {
			return "", "", fmt.Errorf("parse SKILL.md frontmatter: %w", err)
		}
	}
	parsed.Description = strings.TrimSpace(parsed.Description)
	if parsed.Name == "" {
		return "", "", errors.New("SKILL.md frontmatter is missing `name`")
	}
	if parsed.Description == "" {
		return "", "", errors.New("SKILL.md frontmatter is missing `description`")
	}
	if err := ValidateName(parsed.Name); err != nil {
		return "", "", fmt.Errorf("SKILL.md frontmatter `name` %q %w", parsed.Name, err)
	}
	if len(parsed.Description) > MaxSkillDescriptionChars {
		return "", "", fmt.Errorf("SKILL.md frontmatter `description` exceeds %d characters", MaxSkillDescriptionChars)
	}
	return parsed.Name, parsed.Description, nil
}

const utf8BOM = "\ufeff"

func extractFrontmatterBlock(body string) (string, bool) {
	frontmatter, _, ok := SplitFrontmatter(body)
	return frontmatter, ok
}

// SplitFrontmatter parses a YAML frontmatter block delimited by `---`.
func SplitFrontmatter(body string) (frontmatter, rest string, ok bool) {
	trimmed := strings.TrimPrefix(body, utf8BOM)
	if !strings.HasPrefix(trimmed, "---") {
		return "", trimmed, false
	}
	openEnd := strings.Index(trimmed, "\n")
	if openEnd < 0 {
		return "", trimmed, false
	}
	after := trimmed[openEnd+1:]
	for i := 0; i < len(after); {
		nl := strings.Index(after[i:], "\n")
		var line string
		var advance int
		if nl < 0 {
			line = after[i:]
			advance = len(after) - i
		} else {
			line = after[i : i+nl]
			advance = nl + 1
		}
		if strings.TrimRight(line, "\r") == "---" {
			return after[:i], strings.TrimLeft(after[i+advance:], "\n"), true
		}
		i += advance
	}
	return "", trimmed, false
}

func repairUnquotedFrontmatter(frontmatter string) (string, bool) {
	lines := strings.Split(frontmatter, "\n")
	repaired := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "description:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, `"`) ||
			strings.HasPrefix(value, `'`) ||
			strings.HasPrefix(value, "|") ||
			strings.HasPrefix(value, ">") {
			continue
		}
		if !strings.Contains(value, ":") {
			continue
		}
		escaped := strings.ReplaceAll(value, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + `description: "` + escaped + `"`
		repaired = true
	}
	if !repaired {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

type extractPlan struct {
	path  string
	isDir bool
	open  func() (io.ReadCloser, error)
}

func extractZip(raw []byte, absDst string) error {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("read zip archive: %w", err)
	}
	root := ""
	budget := newExtractionBudget()
	plans := make([]extractPlan, 0, len(reader.File))
	for _, file := range reader.File {
		cleaned, err := cleanEntryPath(file.Name)
		if err != nil {
			return err
		}
		if err := budget.accountEntry(cleaned); err != nil {
			return err
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill archive contains a symlink at %q", cleaned)
		}
		segments := strings.SplitN(cleaned, "/", 2)
		if root == "" {
			root = segments[0]
		} else if segments[0] != root {
			return fmt.Errorf("skill archive must contain exactly one top-level directory; got %q and %q", root, segments[0])
		}
		f := file
		plans = append(plans, extractPlan{
			path:  cleaned,
			isDir: strings.HasSuffix(file.Name, "/") || file.FileInfo().IsDir(),
			open:  func() (io.ReadCloser, error) { return f.Open() },
		})
	}
	return materializePlans(absDst, root, plans, budget)
}

func extractTarGz(raw []byte, absDst string) error {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	root := ""
	if err := os.MkdirAll(absDst, 0o755); err != nil {
		return fmt.Errorf("ensure skill destination: %w", err)
	}
	budget := newExtractionBudget()
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		cleaned, err := cleanEntryPath(header.Name)
		if err != nil {
			return err
		}
		if err := budget.accountEntry(cleaned); err != nil {
			return err
		}
		segments := strings.SplitN(cleaned, "/", 2)
		if root == "" {
			root = segments[0]
		} else if segments[0] != root {
			return fmt.Errorf("skill archive must contain exactly one top-level directory; got %q and %q", root, segments[0])
		}
		rel := strings.TrimPrefix(cleaned, root)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		target, err := skillEntryTarget(absDst, rel)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create skill dir %q: %w", rel, err)
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("skill archive contains a symlink at %q", cleaned)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create skill parent dir for %q: %w", rel, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return fmt.Errorf("create skill file %q: %w", rel, err)
			}
			if _, copyErr := budget.copyBounded(out, tr, rel); copyErr != nil {
				_ = out.Close()
				return fmt.Errorf("write skill file %q: %w", rel, copyErr)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("close skill file %q: %w", rel, err)
			}
		default:
			return fmt.Errorf("unsupported tar entry type %q for %q", string(header.Typeflag), header.Name)
		}
	}
	return nil
}

func materializePlans(absDst, root string, plans []extractPlan, budget *extractionBudget) error {
	for _, item := range plans {
		rel := strings.TrimPrefix(item.path, root)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		target, err := skillEntryTarget(absDst, rel)
		if err != nil {
			return err
		}
		if item.isDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create skill dir %q: %w", rel, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create skill parent dir for %q: %w", rel, err)
		}
		body, err := item.open()
		if err != nil {
			return fmt.Errorf("open skill entry %q: %w", rel, err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			_ = body.Close()
			return fmt.Errorf("create skill file %q: %w", rel, err)
		}
		if _, copyErr := budget.copyBounded(out, body, rel); copyErr != nil {
			_ = out.Close()
			_ = body.Close()
			return fmt.Errorf("write skill file %q: %w", rel, copyErr)
		}
		_ = body.Close()
		if err := out.Close(); err != nil {
			return fmt.Errorf("close skill file %q: %w", rel, err)
		}
	}
	return nil
}

func skillEntryTarget(absDst, rel string) (string, error) {
	target := filepath.Join(absDst, filepath.FromSlash(rel))
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve skill entry target: %w", err)
	}
	if absTarget != absDst && !strings.HasPrefix(absTarget, absDst+string(os.PathSeparator)) {
		return "", fmt.Errorf("skill archive entry %q escapes destination", rel)
	}
	return absTarget, nil
}
