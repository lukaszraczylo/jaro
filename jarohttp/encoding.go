// Package jarohttp serves and reads JARO over net/http: content negotiation
// between JARO text, JARO binary and JSON, request body decompression, and a
// response compression middleware (gzip built in, zstd via the zstdenc module).
package jarohttp

import (
	"compress/gzip"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Encoding is one Content-Encoding implementation.
type Encoding struct {
	NewWriter func(io.Writer) (io.WriteCloser, error)
	NewReader func(io.Reader) (io.ReadCloser, error)
	Name      string
	// Priority orders server preference among encodings the client accepts;
	// higher wins.
	Priority int
}

var (
	encMu sync.RWMutex
	encs  = map[string]Encoding{
		"gzip": {
			Name:     "gzip",
			Priority: 1,
			NewWriter: func(w io.Writer) (io.WriteCloser, error) {
				return gzip.NewWriterLevel(w, gzip.DefaultCompression)
			},
			NewReader: func(r io.Reader) (io.ReadCloser, error) { return gzip.NewReader(r) },
		},
	}
)

// RegisterEncoding adds or replaces a Content-Encoding.
func RegisterEncoding(e Encoding) {
	encMu.Lock()
	defer encMu.Unlock()
	encs[e.Name] = e
}

func lookupEncoding(name string) (Encoding, bool) {
	encMu.RLock()
	defer encMu.RUnlock()
	e, ok := encs[strings.ToLower(name)]
	return e, ok
}

type weighted struct {
	val string
	q   float64
}

// parseAccept splits an Accept-style header into values sorted by q, then by
// original order. Entries with q=0 are dropped.
func parseAccept(h string) []weighted {
	var out []weighted
	for _, part := range strings.Split(h, ",") {
		fields := strings.Split(part, ";")
		v := strings.ToLower(strings.TrimSpace(fields[0]))
		if v == "" {
			continue
		}
		q := 1.0
		for _, p := range fields[1:] {
			k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(k, "q") {
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					q = f
				}
			}
		}
		if q > 0 {
			out = append(out, weighted{v, q})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].q > out[j].q })
	return out
}

// pickEncoding chooses the registered encoding the client accepts, by q then
// server Priority.
func pickEncoding(acceptEncoding string) (Encoding, bool) {
	var best Encoding
	bestQ, found := 0.0, false
	for _, w := range parseAccept(acceptEncoding) {
		e, ok := lookupEncoding(w.val)
		if !ok {
			continue
		}
		if !found || w.q > bestQ || (w.q == bestQ && e.Priority > best.Priority) {
			best, bestQ, found = e, w.q, true
		}
	}
	return best, found
}
