package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSkillMd = `---
name: pdf-tools
description: Extract text from PDFs and merge or split files.
---

# PDF Tools

When the user asks about PDFs, use scripts/extract.py to pull text.
`

func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// buildZipWithSymlink builds a zip of regular files plus one symlink entry
// whose content is the link target, matching how zip tools store symlinks.
func buildZipWithSymlink(t *testing.T, files map[string]string, linkName, linkTarget string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	header := &zip.FileHeader{Name: linkName, Method: zip.Deflate}
	header.SetMode(os.ModeSymlink | 0o777)
	f, err := w.CreateHeader(header)
	if err != nil {
		t.Fatalf("zip create symlink %q: %v", linkName, err)
	}
	if _, err := f.Write([]byte(linkTarget)); err != nil {
		t.Fatalf("zip write symlink %q: %v", linkName, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func buildTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(name, "/") {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %q: %v", name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatalf("tar write %q: %v", name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractMetadataZipHappyPath(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"pdf-tools/SKILL.md":           sampleSkillMd,
		"pdf-tools/scripts/extract.py": "print('hi')\n",
	})
	meta, err := ExtractMetadata(FormatZip, raw)
	if err != nil {
		t.Fatalf("ExtractMetadata: %v", err)
	}
	if meta.Name != "pdf-tools" {
		t.Fatalf("name: got %q want pdf-tools", meta.Name)
	}
	if !strings.Contains(meta.Description, "PDFs") {
		t.Fatalf("description: got %q", meta.Description)
	}
	if !strings.Contains(meta.SkillMd, "# PDF Tools") {
		t.Fatalf("SkillMd missing body")
	}
	if meta.RootDir != "pdf-tools" {
		t.Fatalf("root: got %q want pdf-tools", meta.RootDir)
	}
}

func TestExtractMetadataTarGzHappyPath(t *testing.T) {
	raw := buildTarGz(t, map[string]string{
		"pdf-tools/SKILL.md":           sampleSkillMd,
		"pdf-tools/scripts/extract.py": "print('hi')\n",
	})
	meta, err := ExtractMetadata(FormatTarGz, raw)
	if err != nil {
		t.Fatalf("ExtractMetadata: %v", err)
	}
	if meta.Name != "pdf-tools" {
		t.Fatalf("name: got %q want pdf-tools", meta.Name)
	}
}

func TestExtractMetadataRejectsTwoTopLevelDirs(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"pdf-tools/SKILL.md": sampleSkillMd,
		"other/file.txt":     "hi",
	})
	_, err := ExtractMetadata(FormatZip, raw)
	if err == nil {
		t.Fatalf("expected error for two top-level directories")
	}
}

func TestExtractMetadataRejectsMissingSkillMd(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"pdf-tools/README.md": "hi",
	})
	_, err := ExtractMetadata(FormatZip, raw)
	if err == nil {
		t.Fatalf("expected error for missing SKILL.md")
	}
}

func TestExtractMetadataRejectsZipSlip(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"pdf-tools/SKILL.md":      sampleSkillMd,
		"pdf-tools/../etc/passwd": "owned",
	})
	_, err := ExtractMetadata(FormatZip, raw)
	if err == nil {
		t.Fatalf("expected error for path traversal")
	}
}

func TestExtractMetadataRejectsNameRootDirMismatch(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"other-dir/SKILL.md": sampleSkillMd,
	})
	_, err := ExtractMetadata(FormatZip, raw)
	if err == nil || !strings.Contains(err.Error(), "top-level directory") {
		t.Fatalf("expected name/root-dir mismatch error, got %v", err)
	}
}

func TestExtractMetadataRejectsInvalidNameGrammar(t *testing.T) {
	for _, name := range []string{
		"PDF-Tools",
		"pdf_tools",
		"-pdf-tools",
		"pdf-tools-",
		"pdf--tools",
		"pdf tools",
	} {
		t.Run(name, func(t *testing.T) {
			body := "---\nname: " + name + "\ndescription: Extract text from PDFs.\n---\n\n# Skill\n"
			raw := buildZip(t, map[string]string{name + "/SKILL.md": body})
			_, err := ExtractMetadata(FormatZip, raw)
			if err == nil || !strings.Contains(err.Error(), "lowercase alphanumeric") {
				t.Fatalf("expected name grammar error for %q, got %v", name, err)
			}
		})
	}
}

func TestValidateNameLengthBoundary(t *testing.T) {
	if err := ValidateName(strings.Repeat("a", MaxSkillNameChars)); err != nil {
		t.Fatalf("ValidateName at limit: %v", err)
	}
	if err := ValidateName(strings.Repeat("a", MaxSkillNameChars+1)); err == nil {
		t.Fatal("ValidateName above limit succeeded")
	}
}

func TestExtractMetadataRejectsOversizedDescription(t *testing.T) {
	body := "---\nname: pdf-tools\ndescription: " +
		strings.Repeat("x", MaxSkillDescriptionChars+1) +
		"\n---\n\n# Skill\n"
	raw := buildZip(t, map[string]string{"pdf-tools/SKILL.md": body})
	_, err := ExtractMetadata(FormatZip, raw)
	if err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("expected description length error, got %v", err)
	}
}

func TestExtractMetadataRejectsZipSymlink(t *testing.T) {
	raw := buildZipWithSymlink(t, map[string]string{
		"pdf-tools/SKILL.md": sampleSkillMd,
	}, "pdf-tools/link", "/etc/passwd")
	_, err := ExtractMetadata(FormatZip, raw)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestExtractIntoZipRejectsSymlink(t *testing.T) {
	raw := buildZipWithSymlink(t, map[string]string{
		"pdf-tools/SKILL.md": sampleSkillMd,
	}, "pdf-tools/link", "/etc/passwd")
	dst := filepath.Join(t.TempDir(), "skill")
	err := ExtractInto(FormatZip, raw, dst)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dst, "link")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink entry must not be materialized; stat err = %v", statErr)
	}
}

func TestExtractMetadataRejectsTooManyEntries(t *testing.T) {
	files := make(map[string]string, MaxArchiveEntries+8)
	files["skill/SKILL.md"] = sampleSkillMd
	for i := range MaxArchiveEntries {
		files[fmt.Sprintf("skill/f%05d.txt", i)] = "x"
	}
	raw := buildZip(t, files)
	_, err := ExtractMetadata(FormatZip, raw)
	if !errors.Is(err, errExtractionLimitExceeded) {
		t.Fatalf("expected extraction-limit error, got %v", err)
	}
}

func TestExtractMetadataRejectsOversizedExpandedFile(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	skillMd, err := w.Create("skill/SKILL.md")
	if err != nil {
		t.Fatalf("zip create SKILL.md: %v", err)
	}
	if _, err := skillMd.Write([]byte(sampleSkillMd)); err != nil {
		t.Fatalf("zip write SKILL.md: %v", err)
	}
	large, err := w.Create("skill/large.bin")
	if err != nil {
		t.Fatalf("zip create large.bin: %v", err)
	}
	if _, err := io.CopyN(large, zeroReader{}, int64(MaxExtractedFileBytes)+1); err != nil {
		t.Fatalf("zip write large.bin: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	_, err = ExtractMetadata(FormatZip, buf.Bytes())
	if !errors.Is(err, errExtractionLimitExceeded) {
		t.Fatalf("expected extraction-limit error, got %v", err)
	}
}

func TestExtractMetadataDescriptionWithUnquotedColon(t *testing.T) {
	body := `---
name: pdf-tools
description: Use this skill when: the user asks about PDFs.
---
body`
	raw := buildZip(t, map[string]string{"pdf-tools/SKILL.md": body})
	meta, err := ExtractMetadata(FormatZip, raw)
	if err != nil {
		t.Fatalf("ExtractMetadata: %v", err)
	}
	if !strings.Contains(meta.Description, "PDFs") {
		t.Fatalf("description: %q", meta.Description)
	}
}

func TestDetectFormat(t *testing.T) {
	zipBytes := buildZip(t, map[string]string{"pdf-tools/SKILL.md": sampleSkillMd})
	tgzBytes := buildTarGz(t, map[string]string{"pdf-tools/SKILL.md": sampleSkillMd})

	if f, ok := DetectFormat("skill.zip", nil); !ok || f != FormatZip {
		t.Fatalf("zip filename: got (%q,%v)", f, ok)
	}
	if f, ok := DetectFormat("skill.tar.gz", nil); !ok || f != FormatTarGz {
		t.Fatalf("tar.gz filename: got (%q,%v)", f, ok)
	}
	if f, ok := DetectFormat("skill.tgz", nil); !ok || f != FormatTarGz {
		t.Fatalf("tgz filename: got (%q,%v)", f, ok)
	}
	if f, ok := DetectFormat("", zipBytes); !ok || f != FormatZip {
		t.Fatalf("zip magic: got (%q,%v)", f, ok)
	}
	if f, ok := DetectFormat("", tgzBytes); !ok || f != FormatTarGz {
		t.Fatalf("tgz magic: got (%q,%v)", f, ok)
	}
}

func TestExtractIntoZip(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"pdf-tools/SKILL.md":           sampleSkillMd,
		"pdf-tools/scripts/extract.py": "print('hi')\n",
	})
	dst := filepath.Join(t.TempDir(), "skill")
	if err := ExtractInto(FormatZip, raw, dst); err != nil {
		t.Fatalf("ExtractInto: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(body), "PDF Tools") {
		t.Fatalf("SKILL.md body missing")
	}
	if _, err := os.Stat(filepath.Join(dst, "scripts/extract.py")); err != nil {
		t.Fatalf("scripts not extracted: %v", err)
	}
}

func TestExtractIntoTarGz(t *testing.T) {
	raw := buildTarGz(t, map[string]string{
		"pdf-tools/SKILL.md":           sampleSkillMd,
		"pdf-tools/scripts/extract.py": "print('hi')\n",
	})
	dst := filepath.Join(t.TempDir(), "skill")
	if err := ExtractInto(FormatTarGz, raw, dst); err != nil {
		t.Fatalf("ExtractInto: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not extracted: %v", err)
	}
}

func TestVerifyDigestMismatch(t *testing.T) {
	if err := VerifyDigest([]byte("hello"), "sha256:0000"); err == nil {
		t.Fatalf("expected digest mismatch")
	}
}

func TestExtractionBudgetTotalCapTripsCopyBounded(t *testing.T) {
	budget := &extractionBudget{totalRemaining: 5}
	var out strings.Builder
	_, err := budget.copyBounded(&out, strings.NewReader("hello world"), "test/file")
	if !errors.Is(err, errExtractionLimitExceeded) {
		t.Fatalf("expected limit-exceeded error, got %v", err)
	}
	if !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("error message missing 'exceed': %v", err)
	}
}

func TestExtractionBudgetEntryCountCap(t *testing.T) {
	budget := newExtractionBudget()
	for i := range MaxArchiveEntries {
		if err := budget.accountEntry("entry"); err != nil {
			t.Fatalf("entry %d: unexpected error: %v", i, err)
		}
	}
	if err := budget.accountEntry("overflow"); !errors.Is(err, errExtractionLimitExceeded) {
		t.Fatalf("expected limit-exceeded on entry %d, got %v", MaxArchiveEntries+1, err)
	}
}

func TestExtractIntoTarGzRejectsZipBomb(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	payloadSize := int64(MaxExtractedBytes) + 1024
	hdr := &tar.Header{Name: "skill/CANARY.bin", Mode: 0o644, Size: payloadSize, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("zip-bomb tar header: %v", err)
	}
	if _, err := io.CopyN(tw, &zeroReader{}, payloadSize); err != nil {
		t.Fatalf("zip-bomb tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("zip-bomb tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("zip-bomb gz close: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "skill")
	err := ExtractInto(FormatTarGz, buf.Bytes(), dst)
	if !errors.Is(err, errExtractionLimitExceeded) {
		t.Fatalf("expected extraction-limit error, got %v", err)
	}
}

// TestExtractMetadataRejectsTarGzBomb proves the upload-time metadata walk
// rejects an oversized declared entry from its header instead of
// decompressing the payload while advancing to the next entry.
func TestExtractMetadataRejectsTarGzBomb(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	skillHdr := &tar.Header{
		Name:     "skill/SKILL.md",
		Mode:     0o644,
		Size:     int64(len(sampleSkillMd)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(skillHdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(sampleSkillMd)); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	payloadSize := int64(MaxExtractedBytes) + 1024
	hdr := &tar.Header{Name: "skill/BOMB.bin", Mode: 0o644, Size: payloadSize, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("bomb tar header: %v", err)
	}
	if _, err := io.CopyN(tw, &zeroReader{}, payloadSize); err != nil {
		t.Fatalf("bomb tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	_, err := ExtractMetadata(FormatTarGz, buf.Bytes())
	if !errors.Is(err, errExtractionLimitExceeded) {
		t.Fatalf("expected extraction-limit error, got %v", err)
	}
}

func TestExtractMetadataRejectsTarGzTooManyEntries(t *testing.T) {
	files := make(map[string]string, MaxArchiveEntries+8)
	files["skill/SKILL.md"] = sampleSkillMd
	for i := range MaxArchiveEntries {
		files[fmt.Sprintf("skill/f%05d.txt", i)] = "x"
	}
	raw := buildTarGz(t, files)
	_, err := ExtractMetadata(FormatTarGz, raw)
	if !errors.Is(err, errExtractionLimitExceeded) {
		t.Fatalf("expected extraction-limit error, got %v", err)
	}
}

func TestExtractIntoZipRejectsTooManyEntries(t *testing.T) {
	files := make(map[string]string, MaxArchiveEntries+8)
	files["skill/SKILL.md"] = sampleSkillMd
	for i := range MaxArchiveEntries {
		files[fmt.Sprintf("skill/f%05d.txt", i)] = "x"
	}
	raw := buildZip(t, files)
	dst := filepath.Join(t.TempDir(), "skill")
	err := ExtractInto(FormatZip, raw, dst)
	if !errors.Is(err, errExtractionLimitExceeded) {
		t.Fatalf("expected extraction-limit error, got %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
