package taskbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rceman/gpt-github-gateway/internal/task"
)

const (
	SchemaVersion         = 2
	ArchiveFormat         = "tar-gzip"
	ArchiveEncoding       = "base64"
	markerFilename        = ".taskbundle-sha256"
	maxTaskDocumentBytes  = 256 << 10
	maxBundleJSONOverhead = 1 << 20
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

type Archive struct {
	Format                string `json:"format"`
	Encoding              string `json:"encoding"`
	SHA256                string `json:"sha256"`
	CompressedSizeBytes   int64  `json:"compressed_size_bytes"`
	UncompressedSizeBytes int64  `json:"uncompressed_size_bytes"`
	EntryCount            int    `json:"entry_count"`
	Content               string `json:"content"`
}

type TaskDocument struct {
	Title              string   `json:"title"`
	Summary            string   `json:"summary"`
	Objectives         []string `json:"objectives"`
	Constraints        []string `json:"constraints"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	References         []string `json:"references,omitempty"`
}

type Bundle struct {
	SchemaVersion int          `json:"schema_version"`
	TaskID        string       `json:"task_id"`
	GatewayID     string       `json:"gateway_id"`
	ProjectID     string       `json:"project_id"`
	Operation     string       `json:"operation"`
	SubmittedAt   string       `json:"submitted_at"`
	ResultBranch  string       `json:"result_branch"`
	Task          TaskDocument `json:"task"`
	Archive       Archive      `json:"archive"`

	archiveBytes []byte
}

func Load(filename string, maxArchiveBytes int64) (*Bundle, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("task bundle must be a regular non-symlink file")
	}
	maxJSONBytes := maxArchiveBytes*2 + maxBundleJSONOverhead
	if info.Size() <= 0 || info.Size() > maxJSONBytes {
		return nil, fmt.Errorf("task bundle JSON exceeds size limit")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, fmt.Errorf("decode task bundle: %w", err)
	}
	var bundle Bundle
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return nil, fmt.Errorf("decode task bundle: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("task bundle contains trailing JSON data")
	}
	if err := bundle.validate(maxArchiveBytes); err != nil {
		return nil, err
	}
	return &bundle, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("invalid JSON object terminator")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("invalid JSON array terminator")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func (b *Bundle) validate(maxArchiveBytes int64) error {
	if b.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported task bundle schema_version %d", b.SchemaVersion)
	}
	for label, value := range map[string]string{
		"task_id":    b.TaskID,
		"gateway_id": b.GatewayID,
		"project_id": b.ProjectID,
	} {
		if !slugPattern.MatchString(value) {
			return fmt.Errorf("%s must be a safe lowercase slug", label)
		}
	}
	if b.Operation != "apply_patch_pack" {
		return fmt.Errorf("unsupported operation %q", b.Operation)
	}
	if _, err := time.Parse(time.RFC3339, b.SubmittedAt); err != nil {
		return fmt.Errorf("submitted_at must be RFC3339: %w", err)
	}
	if !strings.HasPrefix(b.ResultBranch, "agent/") || strings.ContainsAny(b.ResultBranch, "\x00\r\n ~^:?*[]\\") {
		return fmt.Errorf("result_branch must be a safe agent/ branch")
	}
	if err := b.Task.validate(); err != nil {
		return err
	}
	if b.Archive.Format != ArchiveFormat || b.Archive.Encoding != ArchiveEncoding {
		return fmt.Errorf("archive must use %s with %s encoding", ArchiveFormat, ArchiveEncoding)
	}
	if len(b.Archive.SHA256) != 64 {
		return fmt.Errorf("archive.sha256 must be a lowercase SHA-256")
	}
	if _, err := hex.DecodeString(b.Archive.SHA256); err != nil || strings.ToLower(b.Archive.SHA256) != b.Archive.SHA256 {
		return fmt.Errorf("archive.sha256 must be a lowercase SHA-256")
	}
	if b.Archive.CompressedSizeBytes <= 0 || b.Archive.CompressedSizeBytes > maxArchiveBytes {
		return fmt.Errorf("archive compressed size exceeds limit")
	}
	if b.Archive.UncompressedSizeBytes <= 0 || b.Archive.UncompressedSizeBytes > maxArchiveBytes {
		return fmt.Errorf("archive uncompressed size exceeds limit")
	}
	if b.Archive.EntryCount <= 0 || b.Archive.EntryCount > 10000 {
		return fmt.Errorf("archive entry_count is invalid")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(b.Archive.Content)
	if err != nil {
		return fmt.Errorf("decode archive content: %w", err)
	}
	if int64(len(decoded)) != b.Archive.CompressedSizeBytes {
		return fmt.Errorf("archive compressed size mismatch")
	}
	digest := sha256.Sum256(decoded)
	if hex.EncodeToString(digest[:]) != b.Archive.SHA256 {
		return fmt.Errorf("archive SHA-256 mismatch")
	}
	b.archiveBytes = decoded
	return nil
}

func (d TaskDocument) validate() error {
	if strings.TrimSpace(d.Title) == "" || len([]byte(d.Title)) > 160 {
		return fmt.Errorf("task.title must contain 1 to 160 UTF-8 bytes")
	}
	if strings.TrimSpace(d.Summary) == "" || len([]byte(d.Summary)) > 4096 {
		return fmt.Errorf("task.summary must contain 1 to 4096 UTF-8 bytes")
	}
	for label, values := range map[string][]string{
		"objectives":          d.Objectives,
		"constraints":         d.Constraints,
		"acceptance_criteria": d.AcceptanceCriteria,
	} {
		if err := validateTaskItems(label, values, true); err != nil {
			return err
		}
	}
	if err := validateTaskItems("references", d.References, false); err != nil {
		return err
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encode structured task document: %w", err)
	}
	if len(encoded) > maxTaskDocumentBytes {
		return fmt.Errorf("structured task document exceeds %d bytes", maxTaskDocumentBytes)
	}
	return nil
}

func validateTaskItems(label string, values []string, required bool) error {
	if required && len(values) == 0 {
		return fmt.Errorf("task.%s must be a non-empty array", label)
	}
	if len(values) > 64 {
		return fmt.Errorf("task.%s may contain at most 64 items", label)
	}
	for index, value := range values {
		if strings.TrimSpace(value) == "" || len([]byte(value)) > 2048 {
			return fmt.Errorf("task.%s[%d] must contain 1 to 2048 UTF-8 bytes", label, index)
		}
	}
	return nil
}

func (b *Bundle) Envelope() task.Envelope {
	return task.Envelope{
		SchemaVersion:    1,
		TaskID:           b.TaskID,
		GatewayID:        b.GatewayID,
		ProjectID:        b.ProjectID,
		Operation:        b.Operation,
		SubmittedAt:      b.SubmittedAt,
		RequestPath:      "TASK_REQUEST.json",
		PatchPackPath:    "patch-pack",
		ResultBranch:     b.ResultBranch,
		ApprovalRequired: false,
	}
}

func (b *Bundle) Materialize(localRoot string, maxFileBytes, maxAggregateBytes int64) error {
	if info, err := os.Stat(localRoot); err == nil && info.IsDir() {
		marker, readErr := os.ReadFile(filepath.Join(localRoot, markerFilename))
		if readErr != nil {
			return fmt.Errorf("existing task directory is not an atomic task bundle: %w", readErr)
		}
		if strings.TrimSpace(string(marker)) != b.Archive.SHA256 {
			return fmt.Errorf("task_id_content_changed")
		}
		return nil
	}
	temp := localRoot + ".tmp"
	if err := os.RemoveAll(temp); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(temp, "patch-pack"), 0o700); err != nil {
		return err
	}
	if err := b.extract(filepath.Join(temp, "patch-pack"), maxFileBytes, maxAggregateBytes); err != nil {
		_ = os.RemoveAll(temp)
		return err
	}
	if err := writeJSON(filepath.Join(temp, "task.json"), b.Envelope()); err != nil {
		_ = os.RemoveAll(temp)
		return err
	}
	if err := writeJSON(filepath.Join(temp, "TASK_REQUEST.json"), b.Task); err != nil {
		_ = os.RemoveAll(temp)
		return err
	}
	if err := os.WriteFile(filepath.Join(temp, markerFilename), []byte(b.Archive.SHA256+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(temp)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(localRoot), 0o700); err != nil {
		_ = os.RemoveAll(temp)
		return err
	}
	return os.Rename(temp, localRoot)
}

func (b *Bundle) extract(destination string, maxFileBytes, maxAggregateBytes int64) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(b.archiveBytes))
	if err != nil {
		return fmt.Errorf("open task archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	seen := map[string]bool{}
	caseFolded := map[string]string{}
	required := map[string]bool{
		"AGENT_HANDOFF.md": false,
		"manifest.json":    false,
	}
	var total int64
	entries := 0
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read task archive: %w", nextErr)
		}
		name, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		folded := strings.ToLower(name)
		if seen[name] {
			return fmt.Errorf("archive contains duplicate path %q", name)
		}
		if previous, exists := caseFolded[folded]; exists && previous != name {
			return fmt.Errorf("archive contains case-folding collision %q and %q", previous, name)
		}
		seen[name] = true
		caseFolded[folded] = name
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			entries++
			if header.Size < 0 || header.Size > maxFileBytes {
				return fmt.Errorf("archive file %q exceeds size limit", name)
			}
			total += header.Size
			if total > maxAggregateBytes {
				return fmt.Errorf("archive exceeds aggregate size limit")
			}
			if header.Uid != 0 || header.Gid != 0 || !header.ModTime.Equal(time.Unix(0, 0).UTC()) {
				return fmt.Errorf("archive entry %q is not deterministic", name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if header.Mode&0o111 != 0 {
				mode = 0o700
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(output, tarReader, header.Size)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if written != header.Size {
				return fmt.Errorf("archive file %q size mismatch", name)
			}
			if _, exists := required[name]; exists {
				required[name] = true
			}
		default:
			return fmt.Errorf("archive contains unsupported entry type for %q", name)
		}
	}
	if entries != b.Archive.EntryCount {
		return fmt.Errorf("archive entry_count mismatch")
	}
	if total != b.Archive.UncompressedSizeBytes {
		return fmt.Errorf("archive uncompressed size mismatch")
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("archive is missing required file %s", name)
		}
	}
	return nil
}

func safeArchivePath(raw string) (string, error) {
	if raw == "" || strings.ContainsAny(raw, "\\\x00\r\n") || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("archive contains unsafe path %q", raw)
	}
	clean := path.Clean(raw)
	if clean != raw || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("archive contains unsafe path %q", raw)
	}
	return clean, nil
}

func writeJSON(filename string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filename, data, 0o600)
}
