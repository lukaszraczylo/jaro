package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gojson "github.com/goccy/go-json"
	"github.com/lukaszraczylo/jaro"
)

func BenchmarkDecode(b *testing.B) {
	dir := os.Getenv("JARO_CORPUS")
	if dir == "" {
		b.Skip("set JARO_CORPUS")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // benchmark reads the operator's corpus dir
		if err != nil {
			b.Fatal(err)
		}
		var v any
		if err = json.Unmarshal(raw, &v); err != nil {
			b.Fatal(err)
		}
		text, err := jaro.Marshal(v)
		if err != nil {
			b.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		b.Run("gojson_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				var o any
				if err := gojson.Unmarshal(raw, &o); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("jaro_unmarshal_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := jaro.Unmarshal(text); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("jaro_parse_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := jaro.Parse(text); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
