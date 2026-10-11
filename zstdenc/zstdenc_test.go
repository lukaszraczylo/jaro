package zstdenc

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/lukaszraczylo/jaro"
	"github.com/lukaszraczylo/jaro/jarohttp"
)

func body() map[string]any {
	rows := []any{}
	for i := 0; i < 100; i++ {
		rows = append(rows, map[string]any{"id": int64(i), "name": "user-name-long"})
	}
	return map[string]any{"rows": rows}
}

func TestZstdResponseAndRequest(t *testing.T) {
	Register()
	h := jarohttp.Compress(1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jarohttp.Write(w, r, 200, body())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", jarohttp.MediaText)
	req.Header.Set("Accept-Encoding", "gzip, zstd")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "zstd" {
		t.Fatalf("encoding %q", rec.Header().Get("Content-Encoding"))
	}
	zr, err := zstd.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jaro.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}

	// gzip-only client still gets gzip
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("encoding %q", rec.Header().Get("Content-Encoding"))
	}

	// zstd request body, with decompression-size limit
	enc, _ := zstd.NewWriter(nil)
	plain, _ := jaro.Marshal(body())
	packed := enc.EncodeAll(plain, nil)
	post := httptest.NewRequest("POST", "/", bytes.NewReader(packed))
	post.Header.Set("Content-Type", jarohttp.MediaText)
	post.Header.Set("Content-Encoding", "zstd")
	if _, err := jarohttp.Decode(post, 0); err != nil {
		t.Fatal(err)
	}
	post = httptest.NewRequest("POST", "/", bytes.NewReader(packed))
	post.Header.Set("Content-Type", jarohttp.MediaText)
	post.Header.Set("Content-Encoding", "zstd")
	if _, err := jarohttp.Decode(post, 100); err != jarohttp.ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}
