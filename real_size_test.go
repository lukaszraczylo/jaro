package jaro

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gzipLen(t *testing.T, b []byte) int {
	t.Helper()
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Len()
}

// TestRealSizes prints raw and gzip sizes for every file in $JARO_CORPUS.
func TestRealSizes(t *testing.T) {
	dir := os.Getenv(corpusEnv)
	if dir == "" {
		t.Skip(corpusEnv + " not set")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no corpus files in %s: %v", dir, err)
	}
	var tot [5]int
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // operator-supplied corpus
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err = json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		txt, err := Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		bin, err := MarshalBinary(v)
		if err != nil {
			t.Fatal(err)
		}
		row := [5]int{len(txt), gzipLen(t, txt), len(bin), gzipLen(t, bin), gzipLen(t, raw)}
		for i, n := range row {
			tot[i] += n
		}
		fmt.Printf("size %-16s text %8d gz %7d | bin %8d gz %7d | json gz %7d\n",
			strings.TrimSuffix(filepath.Base(p), ".json"), row[0], row[1], row[2], row[3], row[4])
	}
	fmt.Printf("total text %d gz %d bin %d gz %d json-gz %d\n", tot[0], tot[1], tot[2], tot[3], tot[4])
}
