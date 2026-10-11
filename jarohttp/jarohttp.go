package jarohttp

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/lukaszraczylo/jaro"
)

// Media types. JSON stays the default for clients that do not ask for JARO.
const (
	MediaText   = "application/jaro"
	MediaBinary = "application/jaro-bin"
	MediaJSON   = "application/json"

	// DefaultMaxBody caps both the wire body and its decompressed size.
	DefaultMaxBody int64 = 8 << 20
)

// ErrTooLarge reports a body over the size limit.
var ErrTooLarge = errors.New("jarohttp: body too large")

// ErrUnsupported reports an unknown Content-Type or Content-Encoding.
var ErrUnsupported = errors.New("jarohttp: unsupported media type or encoding")

// Decode reads the request body: it undoes Content-Encoding (gzip, plus any
// registered encoding), then parses by Content-Type (JARO text, JARO binary or
// JSON; empty means JARO text). max <= 0 uses DefaultMaxBody.
func Decode(r *http.Request, max int64) (any, error) {
	if max <= 0 {
		max = DefaultMaxBody
	}
	wire := &capReader{r: r.Body, left: max + 1}
	var body io.Reader = wire
	if enc := strings.TrimSpace(r.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		e, ok := lookupEncoding(enc)
		if !ok {
			return nil, fmt.Errorf("%w: Content-Encoding %q", ErrUnsupported, enc)
		}
		rc, err := e.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("jarohttp: %s body: %w", enc, err)
		}
		defer func() { _ = rc.Close() }()
		body = rc
	}
	data, err := io.ReadAll(io.LimitReader(body, max+1))
	if wire.left <= 0 {
		return nil, ErrTooLarge
	}
	if err != nil {
		return nil, fmt.Errorf("jarohttp: read body: %w", err)
	}
	if int64(len(data)) > max {
		return nil, ErrTooLarge
	}
	ct := r.Header.Get("Content-Type")
	mt := MediaText
	if ct != "" {
		var perr error
		if mt, _, perr = mime.ParseMediaType(ct); perr != nil {
			return nil, fmt.Errorf("%w: Content-Type %q", ErrUnsupported, ct)
		}
	}
	switch mt {
	case MediaText:
		return jaro.Unmarshal(data)
	case MediaBinary:
		return jaro.UnmarshalBinary(data)
	case MediaJSON:
		return jaro.FromJSON(data)
	}
	return nil, fmt.Errorf("%w: Content-Type %q", ErrUnsupported, mt)
}

// Negotiate returns the response media type for an Accept header. JARO is
// chosen only when the client names it; otherwise JSON.
func Negotiate(accept string) string {
	for _, w := range parseAccept(accept) {
		switch w.val {
		case MediaBinary, MediaText, MediaJSON:
			return w.val
		}
	}
	return MediaJSON
}

// Encode renders v in the media type for the request's Accept header.
func Encode(r *http.Request, v any) (body []byte, mediaType string, err error) {
	mediaType = Negotiate(r.Header.Get("Accept"))
	switch mediaType {
	case MediaText:
		body, err = jaro.Marshal(v)
	case MediaBinary:
		body, err = jaro.MarshalBinary(v)
	default:
		body, err = jaro.ToJSON(v)
	}
	return body, mediaType, err
}

// Write negotiates, encodes v and writes it with the given status.
func Write(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, mt, err := Encode(r, v)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Add("Vary", "Accept")
	h.Set("Content-Type", mt)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body) //nolint:gosec // G705 false positive: body is JARO/JSON, never HTML
}

// Validated decodes the request body, checks it against s and calls next with
// the value. Bad bodies get 400 (413 when too large, 415 when unsupported);
// schema violations get 422 with the errors as a list of {path, msg}.
func Validated(s *jaro.Schema, max int64, next func(w http.ResponseWriter, r *http.Request, v any)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := Decode(r, max)
		switch {
		case errors.Is(err, ErrTooLarge):
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		case errors.Is(err, ErrUnsupported):
			http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errs := s.Validate(v); len(errs) > 0 {
			list := make([]any, len(errs))
			for i, e := range errs {
				list[i] = map[string]any{"path": e.Path, "msg": e.Msg}
			}
			Write(w, r, http.StatusUnprocessableEntity, map[string]any{"errors": list})
			return
		}
		next(w, r, v)
	})
}

// capReader reads at most left bytes; left <= 0 afterwards means the source
// had more than the limit allows.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
