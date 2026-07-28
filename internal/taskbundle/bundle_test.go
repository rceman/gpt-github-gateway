package taskbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testEntry struct {
	name     string
	body     string
	typeflag byte
}

func makeArchive(t *testing.T, entries []testEntry) ([]byte, int64, int) {
	t.Helper()
	var raw bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&raw, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	tarWriter := tar.NewWriter(gzipWriter)
	var total int64
	files := 0
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o644,
			Size:     int64(len(entry.body)),
			Typeflag: typeflag,
			ModTime:  time.Unix(0, 0).UTC(),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			files++
			total += int64(len(entry.body))
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes(), total, files
}

func writeBundle(t *testing.T, entries []testEntry) string {
	t.Helper()
	archive, total, files := makeArchive(t, entries)
	digest := sha256.Sum256(archive)
	bundle := Bundle{
		SchemaVersion: SchemaVersion,
		TaskID:        "task_001",
		GatewayID:     "home_pc",
		ProjectID:     "gpt-github-gateway",
		Operation:     "apply_patch_pack",
		SubmittedAt:   "2026-07-28T12:00:00Z",
		ResultBranch:  "agent/task_001",
		Task: TaskDocument{
			Title:              "Apply atomic task bundle",
			Summary:            "Apply and validate the supplied implementation.",
			Objectives:         []string{"Apply the patch pack."},
			Constraints:        []string{"Do not broaden scope."},
			AcceptanceCriteria: []string{"All required gates pass."},
		},
		Archive: Archive{
			Format:                ArchiveFormat,
			Encoding:              ArchiveEncoding,
			SHA256:                hex.EncodeToString(digest[:]),
			CompressedSizeBytes:   int64(len(archive)),
			UncompressedSizeBytes: total,
			EntryCount:            files,
			Content:               base64.StdEncoding.EncodeToString(archive),
		},
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "task.taskbundle.json")
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func validEntries() []testEntry {
	return []testEntry{
		{name: "AGENT_HANDOFF.md", body: "# AGENT_HANDOFF\n"},
		{name: "manifest.json", body: "{}\n"},
		{name: "patch/changes.patch", body: "patch\n"},
	}
}

func TestRejectsDuplicateJSONKeys(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "task.taskbundle.json")
	data := []byte(`{"schema_version":2,"schema_version":2}`)
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filename, 1<<20); err == nil {
		t.Fatal("expected duplicate JSON key to fail")
	}
}

func TestLoadAndMaterializeAtomicBundle(t *testing.T) {
	bundle, err := Load(writeBundle(t, validEntries()), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "task")
	if err := bundle.Materialize(root, 1<<20, 1<<20); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"task.json", "TASK_REQUEST.json", "patch-pack/AGENT_HANDOFF.md", markerFilename} {
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			t.Fatalf("missing %s: %v", relative, err)
		}
	}
	if err := bundle.Materialize(root, 1<<20, 1<<20); err != nil {
		t.Fatalf("same immutable bundle must be idempotent: %v", err)
	}
}

func TestRejectsArchiveTraversal(t *testing.T) {
	entries := append(validEntries(), testEntry{name: "../escape", body: "bad"})
	bundle, err := Load(writeBundle(t, entries), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Materialize(filepath.Join(t.TempDir(), "task"), 1<<20, 1<<20); err == nil {
		t.Fatal("expected traversal archive to fail")
	}
}

func TestRejectsLinksAndCaseFoldCollisions(t *testing.T) {
	cases := [][]testEntry{
		append(validEntries(), testEntry{name: "link", typeflag: tar.TypeSymlink}),
		append(validEntries(), testEntry{name: "Docs/A.txt", body: "a"}, testEntry{name: "docs/a.txt", body: "b"}),
	}
	for _, entries := range cases {
		bundle, err := Load(writeBundle(t, entries), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if err := bundle.Materialize(filepath.Join(t.TempDir(), "task"), 1<<20, 1<<20); err == nil {
			t.Fatal("expected unsafe archive to fail")
		}
	}
}
