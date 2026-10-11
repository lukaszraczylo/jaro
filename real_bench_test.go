package jaro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const corpusEnv = "JARO_CORPUS"

// BenchmarkReal times the codecs on every .json file in $JARO_CORPUS.
func BenchmarkReal(b *testing.B) {
	dir := os.Getenv(corpusEnv)
	if dir == "" {
		b.Skip(corpusEnv + " not set")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		b.Fatalf("no corpus files in %s: %v", dir, err)
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // operator-supplied corpus
		if err != nil {
			b.Fatal(err)
		}
		var v any
		if err = json.Unmarshal(raw, &v); err != nil {
			b.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		txt, err := Marshal(v)
		if err != nil {
			b.Fatal(err)
		}
		bin, err := MarshalBinary(v)
		if err != nil {
			b.Fatal(err)
		}
		b.Run("text_enc_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = Marshal(v)
			}
		})
		b.Run("text_dec_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = Unmarshal(txt)
			}
		})
		b.Run("bin_enc_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = MarshalBinary(v)
			}
		})
		b.Run("bin_dec_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = UnmarshalBinary(bin)
			}
		})
	}
}

// arenaSum walks every node so a benchmark cannot skip the work.
func arenaSum(n Node) int {
	switch n.Kind() {
	case KindString:
		return len(n.String())
	case KindInt:
		return int(n.Int() & 1)
	case KindArray, KindObject:
		t := 0
		for i := 0; i < n.Len(); i++ {
			t += arenaSum(n.Index(i))
		}
		return t
	}
	return 0
}

// BenchmarkRealArena times Parse on every .json file in $JARO_CORPUS.
func BenchmarkRealArena(b *testing.B) {
	dir := os.Getenv(corpusEnv)
	if dir == "" {
		b.Skip(corpusEnv + " not set")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // operator-supplied corpus
		if err != nil {
			b.Fatal(err)
		}
		var v any
		if err = json.Unmarshal(raw, &v); err != nil {
			b.Fatal(err)
		}
		txt, err := Marshal(v)
		if err != nil {
			b.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(p), ".json")
		b.Run("arena_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := Parse(txt); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("reuse_"+name, func(b *testing.B) {
			b.ReportAllocs()
			d := new(Doc)
			for i := 0; i < b.N; i++ {
				if err := d.Reparse(txt); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("arenawalk_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				d, err := Parse(txt)
				if err != nil {
					b.Fatal(err)
				}
				_ = arenaSum(d.Root())
			}
		})
	}
}
