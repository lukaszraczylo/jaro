package jarohttp

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lukaszraczylo/jaro"
)

func sample() any {
	rows := []any{}
	for i := 0; i < 50; i++ {
		rows = append(rows, map[string]any{"id": int64(i), "name": "user-name-long"})
	}
	return map[string]any{"rows": rows}
}

func handler() http.Handler {
	return Compress(1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Write(w, r, 200, sample())
	}))
}

func get(t *testing.T, accept, ae string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if ae != "" {
		req.Header.Set("Accept-Encoding", ae)
	}
	rec := httptest.NewRecorder()
	handler().ServeHTTP(rec, req)
	return rec
}

func TestNegotiate(t *testing.T) {
	cases := map[string]string{
		"":                     MediaJSON,
		"*/*":                  MediaJSON,
		"application/jaro":     MediaText,
		"application/jaro-bin": MediaBinary,
		"application/json;q=0.5, application/jaro": MediaText,
		"text/html, application/jaro-bin;q=0.9":    MediaBinary,
		"application/jaro;q=0":                     MediaJSON,
	}
	for in, want := range cases {
		if got := Negotiate(in); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
}

func TestWriteFormatsAndGzip(t *testing.T) {
	want := sample()
	for _, mt := range []string{MediaText, MediaBinary, MediaJSON} {
		rec := get(t, mt, "gzip")
		if rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("%s: not gzipped", mt)
		}
		if rec.Header().Get("Content-Type") != mt {
			t.Fatalf("%s: content type %s", mt, rec.Header().Get("Content-Type"))
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		var got any
		switch mt {
		case MediaText:
			got, err = jaro.Unmarshal(raw)
		case MediaBinary:
			got, err = jaro.UnmarshalBinary(raw)
		default:
			got, err = jaro.FromJSON(raw)
		}
		if err != nil {
			t.Fatalf("%s: %v", mt, err)
		}
		a, _ := jaro.ToJSON(got)
		b, _ := jaro.ToJSON(want)
		if !bytes.Equal(a, b) {
			t.Fatalf("%s: mismatch", mt)
		}
	}
}

func TestNoGzipWhenNotAccepted(t *testing.T) {
	rec := get(t, MediaText, "")
	if rec.Header().Get("Content-Encoding") != "" || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("headers %v", rec.Header())
	}
	if _, err := jaro.Unmarshal(rec.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestSmallResponseStaysIdentity(t *testing.T) {
	h := Compress(0, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { Write(w, r, 200, map[string]any{"a": int64(1)}) }))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept", MediaText)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "a:1" || rec.Header().Get("Content-Length") != "3" {
		t.Fatalf("%v %q", rec.Header(), rec.Body.String())
	}
}

func TestAlreadyEncodedAndBinaryAsset(t *testing.T) {
	h := Compress(1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte{1}, 1000))
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 1000 {
		t.Fatal("png was compressed")
	}
}

func post(t *testing.T, body []byte, ct, ce string) *http.Request {
	t.Helper()
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	if ce != "" {
		req.Header.Set("Content-Encoding", ce)
	}
	return req
}

func TestDecode(t *testing.T) {
	txt, _ := jaro.Marshal(sample())
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(txt)
	_ = zw.Close()
	bin, _ := jaro.MarshalBinary(sample())
	js, _ := jaro.ToJSON(sample())
	for name, req := range map[string]*http.Request{
		"text":   post(t, txt, MediaText, ""),
		"gzip":   post(t, gz.Bytes(), MediaText+"; charset=utf-8", "gzip"),
		"binary": post(t, bin, MediaBinary, ""),
		"json":   post(t, js, MediaJSON, ""),
	} {
		if _, err := Decode(req, 0); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Decode(post(t, txt, "text/csv", ""), 0); err == nil {
		t.Error("csv accepted")
	}
	if _, err := Decode(post(t, txt, MediaText, "br"), 0); err == nil {
		t.Error("br accepted")
	}
	if _, err := Decode(post(t, txt, MediaText, ""), 10); err != ErrTooLarge {
		t.Errorf("want ErrTooLarge, got %v", err)
	}
}

func TestDecodeGzipBomb(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(bytes.Repeat([]byte("a "), 5<<20))
	_ = zw.Close()
	if gz.Len() > 100<<10 {
		t.Skip("not compressible enough")
	}
	if _, err := Decode(post(t, gz.Bytes(), MediaText, "gzip"), 1<<20); err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestValidated(t *testing.T) {
	def, _ := jaro.Unmarshal([]byte(`id:"int(1..)" name:str`))
	s, _ := jaro.Compile(def)
	called := false
	h := Validated(s, 0, func(w http.ResponseWriter, r *http.Request, v any) {
		called = true
		Write(w, r, 201, v)
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, post(t, []byte(`id:5 name:ann`), MediaText, ""))
	if rec.Code != 201 || !called {
		t.Fatalf("code %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req := post(t, []byte(`id:0`), MediaText, "")
	req.Header.Set("Accept", MediaText)
	h.ServeHTTP(rec, req)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "$.id") {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, post(t, []byte(`{`), MediaText, ""))
	if rec.Code != 400 {
		t.Fatalf("code %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, post(t, []byte(`x`), "text/csv", ""))
	if rec.Code != 415 {
		t.Fatalf("code %d", rec.Code)
	}
}

// TestRealServer runs the stack over a TCP listener with a stock net/http
// client, including gzip request and response bodies.
func TestRealServer(t *testing.T) {
	def, _ := jaro.Unmarshal([]byte(`id:"int(1..)" name:str`))
	schema, _ := jaro.Compile(def)
	mux := http.NewServeMux()
	mux.Handle("/echo", Validated(schema, 0, func(w http.ResponseWriter, r *http.Request, v any) { Write(w, r, 200, v) }))
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) { Write(w, r, 200, sample()) })
	srv := httptest.NewServer(Compress(1, mux))
	defer srv.Close()

	// Transport with DisableCompression so we see the raw Content-Encoding.
	c := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	req, _ := http.NewRequest("GET", srv.URL+"/big", nil)
	req.Header.Set("Accept", MediaBinary)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Content-Type") != MediaBinary || resp.Header.Get("Vary") == "" {
		t.Fatalf("headers: %v", resp.Header)
	}
	zr, _ := gzip.NewReader(resp.Body)
	raw, _ := io.ReadAll(zr)
	if _, uerr := jaro.UnmarshalBinary(raw); uerr != nil {
		t.Fatal(uerr)
	}

	// gzip-compressed JARO request, JARO response
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`id:7 name:ann`))
	_ = zw.Close()
	req, _ = http.NewRequest("POST", srv.URL+"/echo", &gz)
	req.Header.Set("Content-Type", MediaText)
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Accept", MediaText)
	resp2, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	out, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != 200 || string(out) != "id:7 name:ann" {
		t.Fatalf("%d %q", resp2.StatusCode, out)
	}

	// invalid body -> 422 with path
	req, _ = http.NewRequest("POST", srv.URL+"/echo", strings.NewReader(`id:0 name:x`))
	req.Header.Set("Content-Type", MediaText)
	resp3, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	b3, _ := io.ReadAll(resp3.Body)
	if resp3.StatusCode != 422 || !strings.Contains(string(b3), "$.id") {
		t.Fatalf("%d %s", resp3.StatusCode, b3)
	}
}
