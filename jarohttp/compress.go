package jarohttp

import (
	"io"
	"net/http"
	"strconv"
	"strings"
)

// DefaultMinSize is the response size below which Compress sends identity.
const DefaultMinSize = 256

// Compress wraps next with response compression. It picks the best registered
// encoding from Accept-Encoding, skips responses that already carry a
// Content-Encoding, are smaller than minSize (<= 0 uses DefaultMinSize) or are
// not text-like (JARO, JSON, text/*), and sets Vary: Accept-Encoding.
func Compress(minSize int, next http.Handler) http.Handler {
	if minSize <= 0 {
		minSize = DefaultMinSize
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		enc, ok := pickEncoding(r.Header.Get("Accept-Encoding"))
		if !ok || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w, enc: enc, min: minSize}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

type compressWriter struct {
	http.ResponseWriter
	w      io.WriteCloser
	enc    Encoding
	buf    []byte
	min    int
	status int
	mode   int // 0 undecided, 1 passthrough, 2 compressing
}

func (c *compressWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	if c.mode == 0 && (code < 200 || code == http.StatusNoContent || code == http.StatusNotModified) {
		c.mode = 1
		c.ResponseWriter.WriteHeader(code)
	}
}

func (c *compressWriter) Write(p []byte) (int, error) {
	switch c.mode {
	case 1:
		return c.ResponseWriter.Write(p)
	case 2:
		return c.w.Write(p)
	}
	c.buf = append(c.buf, p...)
	if len(c.buf) >= c.min {
		if err := c.decide(true); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func compressible(ct string) bool {
	ct = strings.ToLower(ct)
	return ct == "" || strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") ||
		strings.HasPrefix(ct, MediaText) || strings.HasPrefix(ct, MediaBinary) ||
		strings.Contains(ct, "xml") || strings.Contains(ct, "javascript")
}

// decide commits to compression or passthrough and flushes the buffer.
func (c *compressWriter) decide(compress bool) error {
	h := c.Header()
	if compress && (h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) || c.status == http.StatusPartialContent) {
		compress = false
	}
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if !compress {
		c.mode = 1
		if h.Get("Content-Length") == "" {
			h.Set("Content-Length", strconv.Itoa(len(c.buf)))
			if len(c.buf) == 0 {
				h.Del("Content-Length")
			}
		}
		c.ResponseWriter.WriteHeader(c.status)
		_, err := c.ResponseWriter.Write(c.buf) //nolint:gosec // G705 false positive: handler bytes pass through unchanged
		c.buf = nil
		return err
	}
	zw, err := c.enc.NewWriter(c.ResponseWriter)
	if err != nil {
		c.mode = 1
		c.ResponseWriter.WriteHeader(http.StatusInternalServerError)
		return err
	}
	h.Set("Content-Encoding", c.enc.Name)
	h.Del("Content-Length")
	h.Del("Etag") // a strong validator no longer matches the encoded bytes
	c.ResponseWriter.WriteHeader(c.status)
	c.w, c.mode = zw, 2
	_, err = zw.Write(c.buf)
	c.buf = nil
	return err
}

func (c *compressWriter) finish() {
	switch c.mode {
	case 0:
		_ = c.decide(false) // under minSize: identity
	case 2:
		_ = c.w.Close()
	}
}

// Flush supports streaming handlers; it forces the compress decision.
func (c *compressWriter) Flush() {
	if c.mode == 0 {
		_ = c.decide(true)
	}
	if f, ok := c.w.(interface{ Flush() error }); ok && c.mode == 2 {
		_ = f.Flush()
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
