// Package jaro implements JARO, a compact data format with a token-lean
// text form and a fast, small binary form. Both carry the JSON data model.
package jaro

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxDepth     = 512
	maxInterned  = 4096
	absentCell   = '^' // table cell for a key the row does not have
	textPresent  = 0.5 // smallest share of present cells for a text sparse table
	binPresent   = 0.8 // smallest share of present cells for a binary sparse table
	smallArray   = 8   // arrays up to this length share allocation chunks
	cellChunk    = 256 // cells per chunk
	maxInternLen = 48
	escChunk     = 16384 // bytes per buffer of unescaped strings
)

// Marshal encodes v (nil, bool, ints, floats, string, []any, map[string]any
// and typed slices/maps of those) as JARO text.
func Marshal(v any) ([]byte, error) { return MarshalOpts(v, Options{}) }

// MarshalOpts is Marshal with options.
func MarshalOpts(v any, o Options) ([]byte, error) {
	e := &textEnc{buf: getBuf()}
	defer func() { putBuf(e.buf) }()
	if o.Dedup {
		e.dd = newDedup(v, o.minDedup(), false)
	}
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		if err := e.objBody(m, 1); err != nil {
			return nil, err
		}
		return bytes.Clone(e.buf), nil
	}
	if err := e.value(v, 0); err != nil {
		return nil, err
	}
	return bytes.Clone(e.buf), nil
}

type textEnc struct {
	dd  *dedup
	buf []byte
	ks  []string // scratch stack of sorted object keys
}

func (e *textEnc) value(v any, depth int) error {
	if depth > maxDepth {
		return errors.New("jaro: nesting too deep")
	}
	if e.dd != nil {
		if idx, use, def := e.dd.ref(v); use {
			e.buf = append(e.buf, '*', 'a')
			e.buf = strconv.AppendUint(e.buf, idx, 10)
			return nil
		} else if def {
			e.buf = append(e.buf, '&', 'a')
			e.buf = strconv.AppendUint(e.buf, idx, 10)
			e.buf = append(e.buf, ' ')
		}
	}
	switch x := v.(type) {
	case nil:
		e.buf = append(e.buf, '~')
	case bool:
		if x {
			e.buf = append(e.buf, "true"...)
		} else {
			e.buf = append(e.buf, "false"...)
		}
	case string:
		e.str(x)
	case map[string]any:
		if specs, ok := keyedSpecs(x); ok {
			return e.keyedTable(x, specs, depth)
		}
		e.buf = append(e.buf, '{')
		if err := e.objBody(x, depth+1); err != nil {
			return err
		}
		e.buf = append(e.buf, '}')
	case []any:
		return e.array(x, depth)
	default:
		n, ok, err := normNum(v)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("jaro: unsupported type %T", v)
		}
		if n.isInt {
			e.buf = strconv.AppendInt(e.buf, n.i, 10)
		} else {
			e.buf = strconv.AppendFloat(e.buf, n.f, 'g', -1, 64)
		}
	}
	return nil
}

func (e *textEnc) objBody(m map[string]any, depth int) error {
	start := len(e.ks)
	e.ks = appendSortedKeys(e.ks, m)
	keys := e.ks[start:]
	for i, k := range keys {
		if i > 0 {
			e.buf = append(e.buf, ' ')
		}
		e.str(k)
		e.buf = append(e.buf, ':')
		if err := e.value(m[k], depth); err != nil {
			return err
		}
	}
	e.ks = e.ks[:start]
	return nil
}

func (e *textEnc) array(a []any, depth int) error {
	e.buf = append(e.buf, '[')
	if specs, ok := tableSpecs(a); ok {
		if err := e.table(a, specs, depth); err != nil {
			return err
		}
	} else {
		for i, x := range a {
			if i > 0 {
				e.buf = append(e.buf, ' ')
			}
			if err := e.value(x, depth+1); err != nil {
				return err
			}
		}
	}
	e.buf = append(e.buf, ']')
	return nil
}

// sparseKeys returns the union of keys when a holds at least two objects and
// at least half of the table cells would be present.
func sparseKeys(a []any, minPresent float64) ([]string, bool) {
	if len(a) < 2 {
		return nil, false
	}
	seen := map[string]struct{}{}
	present := 0
	for _, x := range a {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, false
		}
		present += len(m)
		for k := range m {
			seen[k] = struct{}{}
		}
	}
	if len(seen) == 0 || float64(present) < minPresent*float64(len(seen)*len(a)) {
		return nil, false
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, true
}

func (e *textEnc) str(s string) {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	if !needsQuote(s) {
		e.buf = append(e.buf, s...)
		return
	}
	e.buf = append(e.buf, '"')
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			e.buf = append(e.buf, '\\', c)
			i++
		case c == '\n':
			e.buf = append(e.buf, '\\', 'n')
			i++
		case c == '\r':
			e.buf = append(e.buf, '\\', 'r')
			i++
		case c == '\t':
			e.buf = append(e.buf, '\\', 't')
			i++
		case c < 0x20:
			e.buf = append(e.buf, '\\', 'u', '0', '0', hexd[c>>4], hexd[c&15])
			i++
		case c < utf8.RuneSelf:
			e.buf = append(e.buf, c)
			i++
		default:
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				e.buf = append(e.buf, "\xef\xbf\xbd"...)
			} else {
				e.buf = append(e.buf, s[i:i+n]...)
			}
			i += n
		}
	}
	e.buf = append(e.buf, '"')
}

const hexd = "0123456789abcdef"

var delimTable = func() (t [256]bool) {
	for c := range t {
		t[c] = c < 0x20
	}
	for _, c := range " \t\n\r\"{}[]|:\\#" {
		t[c] = true
	}
	return t
}()

func isDelim(c byte) bool { return delimTable[c] }

// needsQuote reports whether s must be quoted. s must be valid UTF-8.
func needsQuote(s string) bool {
	if s == "" || s == "~" || s == "^" || s == "true" || s == "false" || s[0] == '&' || s[0] == '*' {
		return true
	}
	for i := 0; i < len(s); i++ {
		if isDelim(s[i]) {
			return true
		}
	}
	return numLike(s)
}

// numLike reports whether a bare token would decode as a number.
func numLike(s string) bool {
	i := 0
	if s[0] == '-' {
		i = 1
	}
	if i >= len(s) {
		return false
	}
	c := s[i]
	return (c >= '0' && c <= '9') || (c == '.' && i+1 < len(s))
}

type num struct {
	i     int64
	f     float64
	isInt bool
}

func normNum(v any) (num, bool, error) {
	switch x := v.(type) {
	case int:
		return num{i: int64(x), isInt: true}, true, nil
	case int8:
		return num{i: int64(x), isInt: true}, true, nil
	case int16:
		return num{i: int64(x), isInt: true}, true, nil
	case int32:
		return num{i: int64(x), isInt: true}, true, nil
	case int64:
		return num{i: x, isInt: true}, true, nil
	case uint:
		return uintNum(uint64(x))
	case uint8:
		return num{i: int64(x), isInt: true}, true, nil
	case uint16:
		return num{i: int64(x), isInt: true}, true, nil
	case uint32:
		return num{i: int64(x), isInt: true}, true, nil
	case uint64:
		return uintNum(x)
	case float32:
		return floatNum(float64(x))
	case float64:
		return floatNum(x)
	}
	return num{}, false, nil
}

func uintNum(u uint64) (num, bool, error) {
	if u > math.MaxInt64 {
		return floatNum(float64(u))
	}
	return num{i: int64(u), isInt: true}, true, nil
}

func floatNum(f float64) (num, bool, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return num{}, true, errors.New("jaro: NaN/Inf not representable")
	}
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return num{i: int64(f), isInt: true}, true, nil
	}
	return num{f: f}, true, nil
}

// appendSortedKeys appends the sorted keys of m to dst.
func appendSortedKeys(dst []string, m map[string]any) []string {
	n := len(dst)
	for k := range m {
		dst = append(dst, k)
	}
	sort.Strings(dst[n:])
	return dst
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// tableKeys returns the shared sorted key set if a is >=2 non-empty objects
// with identical keys.
func tableKeys(a []any) ([]string, bool) {
	if len(a) < 2 {
		return nil, false
	}
	first, ok := a[0].(map[string]any)
	if !ok || len(first) == 0 {
		return nil, false
	}
	for _, x := range a[1:] {
		m, ok := x.(map[string]any)
		if !ok || len(m) != len(first) {
			return nil, false
		}
		for k := range first {
			if _, ok := m[k]; !ok {
				return nil, false
			}
		}
	}
	return sortedKeys(first), true
}

// tableData is tableKeys plus the cell values, column-major, gathered with a
// single map lookup per cell.
func tableData(a []any) (keys []string, cols [][]any, ok bool) {
	if len(a) < 2 {
		return nil, nil, false
	}
	first, isMap := a[0].(map[string]any)
	if !isMap || len(first) == 0 {
		return nil, nil, false
	}
	keys = sortedKeys(first)
	cols = make([][]any, len(keys))
	cells := make([]any, len(keys)*len(a))
	for j := range cols {
		cols[j] = cells[j*len(a) : (j+1)*len(a)]
	}
	for i, x := range a {
		m, isMap := x.(map[string]any)
		if !isMap || len(m) != len(keys) {
			return nil, nil, false
		}
		for j, k := range keys {
			v, present := m[k]
			if !present {
				return nil, nil, false
			}
			cols[j][i] = v
		}
	}
	return keys, cols, true
}

// Unmarshal decodes JARO text into nil, bool, int64, float64, string,
// []any or map[string]any.
func Unmarshal(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("jaro: input is not valid UTF-8")
	}
	d := &textDec{s: data, str: string(data), budget: maxExpand}
	d.ws()
	if d.pos >= len(d.s) {
		return nil, errors.New("jaro: empty input")
	}
	if d.atKey() {
		m, err := d.objBody(0, 0)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
	v, err := d.value(0)
	if err != nil {
		return nil, err
	}
	d.ws()
	if d.pos != len(d.s) {
		return nil, d.errf("trailing data")
	}
	return v, nil
}

type textDec struct {
	cellArena
	box      slab
	esc      strings.Builder
	str      string
	anchors  map[string]any
	interned map[string]any
	stk      []any // elements of the arrays being parsed
	s        []byte
	pos      int
	budget   int
}

// cellArena hands out zeroed, non-nil []any slices. Short slices share chunks
// so a document of many small arrays makes one allocation per chunk.
type cellArena struct{ chunk []any }

func (a *cellArena) cells(n int) []any {
	if n == 0 || n > smallArray {
		return make([]any, n)
	}
	if len(a.chunk) < n {
		a.chunk = make([]any, cellChunk)
	}
	out := a.chunk[:n:n]
	a.chunk = a.chunk[n:]
	return out
}

func (d *textDec) errf(format string, a ...any) error {
	return fmt.Errorf("jaro: offset %d: %s", d.pos, fmt.Sprintf(format, a...))
}

func (d *textDec) ws() {
	for d.pos < len(d.s) {
		switch d.s[d.pos] {
		case ' ', '\t', '\n', '\r':
			d.pos++
		case '#':
			for d.pos < len(d.s) && d.s[d.pos] != '\n' {
				d.pos++
			}
		default:
			return
		}
	}
}

// atKey reports whether the next token is followed by ':' (an object key).
func (d *textDec) atKey() bool {
	p := d.pos
	if p < len(d.s) && d.s[p] == '"' {
		p++
		for p < len(d.s) && d.s[p] != '"' {
			if d.s[p] == '\\' {
				p++
			}
			p++
		}
		p++
	} else {
		for p < len(d.s) && !isDelim(d.s[p]) {
			p++
		}
	}
	return p < len(d.s) && d.s[p] == ':'
}

func (d *textDec) objBody(depth int, end byte) (map[string]any, error) {
	m := make(map[string]any)
	var explicit map[string]struct{} // member names, kept once a spread can add keys of its own
	for {
		d.ws()
		if d.pos >= len(d.s) {
			if end == 0 {
				return m, nil
			}
			return nil, d.errf("unterminated object")
		}
		if end != 0 && d.s[d.pos] == end {
			d.pos++
			return m, nil
		}
		if d.s[d.pos] == '*' {
			if explicit == nil {
				explicit = make(map[string]struct{}, len(m))
				for k := range m {
					explicit[k] = struct{}{}
				}
			}
			if err := d.spread(m); err != nil {
				return nil, err
			}
			continue
		}
		k, err := d.token()
		if err != nil {
			return nil, err
		}
		ks, ok := k.(string)
		if !ok || d.pos >= len(d.s) || d.s[d.pos] != ':' {
			return nil, d.errf("expected key:")
		}
		d.pos++
		v, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		if explicit != nil {
			if _, dup := explicit[ks]; dup {
				return nil, d.errf("duplicate key %q", ks)
			}
			explicit[ks] = struct{}{}
			m[ks] = v
			continue
		}
		before := len(m)
		m[ks] = v
		if len(m) == before {
			return nil, d.errf("duplicate key %q", ks)
		}
	}
}

func (d *textDec) value(depth int) (any, error) {
	if depth > maxDepth {
		return nil, d.errf("nesting too deep")
	}
	d.ws()
	if d.pos >= len(d.s) {
		return nil, d.errf("unexpected end")
	}
	switch d.s[d.pos] {
	case '&':
		return d.anchor(depth)
	case '*':
		return d.deref()
	case '{':
		d.pos++
		if d.ws(); d.pos < len(d.s) && d.s[d.pos] == '|' {
			d.pos++
			return d.keyedTable(depth)
		}
		return d.objBody(depth, '}')
	case '[':
		d.pos++
		return d.array(depth)
	case '}', ']', '|', ':':
		return nil, d.errf("unexpected %q", d.s[d.pos])
	}
	return d.token()
}

func (d *textDec) array(depth int) (any, error) {
	d.ws()
	if d.pos < len(d.s) && d.s[d.pos] == '|' {
		d.pos++
		specs, err := d.specList('|', 0)
		if err != nil {
			return nil, err
		}
		return d.tableRows(depth, specs)
	}
	start := len(d.stk)
	for {
		d.ws()
		if d.pos >= len(d.s) {
			return nil, d.errf("unterminated array")
		}
		if d.s[d.pos] == ']' {
			d.pos++
			out := d.cells(len(d.stk) - start)
			copy(out, d.stk[start:])
			d.stk = d.stk[:start]
			return out, nil
		}
		v, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		d.stk = append(d.stk, v)
	}
}

// token reads a quoted or bare scalar. A quoted token is always a string.
func (d *textDec) token() (any, error) {
	if d.s[d.pos] == '"' {
		return d.quoted()
	}
	start := d.pos
	p, b := start, d.s
	for p < len(b) && !delimTable[b[p]] {
		p++
	}
	d.pos = p
	if d.pos == start {
		return nil, d.errf("unexpected %q", d.s[d.pos])
	}
	raw := d.s[start:d.pos]
	if v, ok := d.fastScalar(raw); ok {
		return v, nil
	}
	if c := raw[0]; c != '-' && c != '.' && (c < '0' || c > '9') {
		if v, ok := d.interned[string(raw)]; ok { // number-like tokens must still be typed
			return v, nil
		}
	}
	tok := d.str[start:d.pos] // substring of the one input copy: no per-token allocation
	switch tok {
	case "~":
		return nil, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	if numLike(tok) && isNumber(tok) {
		if isIntToken(tok) {
			if i, err := strconv.ParseInt(tok, 10, 64); err == nil {
				return i, nil
			}
		}
		if f, err := strconv.ParseFloat(tok, 64); err == nil && !math.IsInf(f, 0) {
			if f == 0 {
				return int64(0), nil
			}
			return d.box.float(f), nil
		}
	}
	return d.intern(tok), nil
}

// intern remembers a boxed bare string so repeats cost no allocation. The
// table is bounded so unique-heavy documents do not grow it without limit.
func (d *textDec) intern(tok string) any {
	v := d.box.str(tok) // boxed once; the table and the caller share it
	if len(tok) > maxInternLen || len(d.interned) >= maxInterned {
		return v
	}
	if d.interned == nil {
		d.interned = make(map[string]any, 256)
	}
	d.interned[tok] = v
	return v
}

func (d *textDec) quoted() (any, error) {
	d.pos++
	start := d.pos
	if end := bytes.IndexByte(d.s[start:], '"'); end >= 0 && bytes.IndexByte(d.s[start:start+end], '\\') < 0 {
		end += start
		d.pos = end + 1
		if end-start > maxInternLen {
			return d.box.str(d.str[start:end]), nil
		}
		if v, ok := d.interned[string(d.s[start:end])]; ok {
			return v, nil
		}
		return d.intern(d.str[start:end]), nil
	}
	if i := bytes.IndexByte(d.s[start:], '\\'); i >= 0 {
		d.pos = start + i
	} else {
		d.pos = len(d.s)
	}
	sb := d.escBuf(start)
	base := sb.Len()
	if err := d.unescape(sb, start); err != nil {
		return nil, err
	}
	return d.box.str(sb.String()[base:]), nil
}

// unescape appends the decoded rest of a quoted string to sb and consumes the
// closing quote. d.s[start:d.pos] is the part before the first escape.
func (d *textDec) unescape(sb *strings.Builder, start int) error {
	sb.Write(d.s[start:d.pos])
	q := -1 // next '"' at or after d.pos, len(d.s) if none
	for d.pos < len(d.s) {
		if q < d.pos {
			if q = bytes.IndexByte(d.s[d.pos:], '"'); q < 0 {
				q = len(d.s)
			} else {
				q += d.pos
			}
		}
		end := q
		if i := bytes.IndexByte(d.s[d.pos:q], '\\'); i >= 0 {
			end = d.pos + i
		}
		if end > d.pos {
			sb.Write(d.s[d.pos:end])
			d.pos = end
			continue
		}
		c := d.s[d.pos]
		d.pos++
		switch c {
		case '"':
			return nil
		case '\\':
			if d.pos >= len(d.s) {
				return d.errf("bad escape")
			}
			e := d.s[d.pos]
			d.pos++
			switch e {
			case '"', '\\':
				sb.WriteByte(e)
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'u':
				if d.pos+4 > len(d.s) {
					return d.errf("bad \\u escape")
				}
				n, ok := hex4(d.s[d.pos : d.pos+4])
				if !ok {
					return d.errf("bad \\u escape")
				}
				d.pos += 4
				if n >= 0xD800 && n < 0xE000 {
					lo, ok := d.lowSurrogate(n)
					if !ok {
						return d.errf("unpaired surrogate escape")
					}
					n = lo
				}
				sb.WriteRune(n)
			default:
				return d.errf("bad escape \\%c", e)
			}
		}
	}
	return d.errf("unterminated string")
}

// escBuf returns the shared builder that holds unescaped strings, with room
// for the string that starts at start. Earlier strings keep their old buffer.
func (d *textDec) escBuf(start int) *strings.Builder {
	end := d.pos
	for end < len(d.s) && d.s[end] != '"' {
		if d.s[end] == '\\' {
			end++
		}
		end++
	}
	if need := min(end, len(d.s)) - start; d.esc.Cap()-d.esc.Len() < need {
		d.esc = strings.Builder{}
		d.esc.Grow(max(need, escChunk))
	}
	return &d.esc
}

func (d *textDec) name() (string, error) {
	d.pos++
	start := d.pos
	for d.pos < len(d.s) && !isDelim(d.s[d.pos]) {
		d.pos++
	}
	if d.pos == start {
		return "", d.errf("missing anchor name")
	}
	return string(d.s[start:d.pos]), nil
}

func (d *textDec) anchor(depth int) (any, error) {
	n, err := d.name()
	if err != nil {
		return nil, err
	}
	if _, dup := d.anchors[n]; dup {
		return nil, d.errf("anchor &%s defined twice", n)
	}
	v, err := d.value(depth + 1)
	if err != nil {
		return nil, err
	}
	if d.anchors == nil {
		d.anchors = make(map[string]any)
	}
	d.anchors[n] = v
	return v, nil
}

func (d *textDec) deref() (any, error) {
	n, err := d.name()
	if err != nil {
		return nil, err
	}
	v, ok := d.anchors[n]
	if !ok {
		return nil, d.errf("undefined anchor *%s", n)
	}
	return deepCopy(v, &d.budget)
}

// spread merges the object behind *name into m; later keys override.
func (d *textDec) spread(m map[string]any) error {
	v, err := d.deref()
	if err != nil {
		return err
	}
	src, ok := v.(map[string]any)
	if !ok {
		return d.errf("spread needs an object anchor")
	}
	for k, x := range src {
		m[k] = x
	}
	return nil
}

// isIntToken reports whether tok is only an optional '-' and digits, so
// ParseInt is not asked to fail (a failed parse allocates its error).
func isIntToken(tok string) bool {
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; (c < '0' || c > '9') && (i != 0 || c != '-') {
			return false
		}
	}
	return true
}

// fastScalar decodes the common scalar tokens without allocating a string:
// ~, true, false and the plain numbers fastNum accepts.
func (d *textDec) fastScalar(b []byte) (any, bool) {
	switch string(b) {
	case "~":
		return nil, true
	case "true":
		return true, true
	case "false":
		return false, true
	}
	i, f, isInt, ok := fastNum(b)
	switch {
	case !ok:
		return nil, false
	case isInt:
		return d.box.int(i), true
	}
	return d.box.float(f), true
}

// fastNum decodes plain decimal ints of up to 18 digits and plain decimals
// (digits '.' digits, at most 15 digits in all). Decimals use the exact
// mantissa/10^k path, which is correctly rounded because mantissa < 2^53 and
// k <= 22. A decimal that is zero is the integer 0, like the encoder writes it.
func fastNum(b []byte) (i int64, f float64, isInt, ok bool) {
	p, neg := 0, false
	if b[0] == '-' {
		p, neg = 1, true
	}
	if n := len(b) - p; n == 0 || n > 18 || (n > 16 && bytes.IndexByte(b, '.') >= 0) {
		return 0, 0, false, false // empty, too long for an int64, or a decimal with over 15 digits
	}
	var v int64
	dot, digits := -1, 0
	for ; p < len(b); p++ {
		c := b[p]
		if c == '.' && dot < 0 && digits > 0 {
			dot = p
			continue
		}
		if c < '0' || c > '9' {
			return 0, 0, false, false
		}
		v = v*10 + int64(c-'0')
		digits++
	}
	if dot < 0 {
		if neg {
			v = -v
		}
		return v, 0, true, true
	}
	k := len(b) - dot - 1
	if k == 0 || digits > 15 {
		return 0, 0, false, false
	}
	f = float64(v) / pow10[k]
	if f == 0 {
		return 0, 0, true, true
	}
	if neg {
		f = -f
	}
	return 0, f, false, true
}

// hex4 parses exactly four hex digits.
func hex4(b []byte) (rune, bool) {
	var n rune
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			c = c - 'A' + 10
		default:
			return 0, false
		}
		n = n<<4 | rune(c)
	}
	return n, true
}

// lowSurrogate reads the \uXXXX that must follow the high surrogate hi and
// returns the combined code point.
func (d *textDec) lowSurrogate(hi rune) (rune, bool) {
	if hi >= 0xDC00 || d.pos+6 > len(d.s) || d.s[d.pos] != '\\' || d.s[d.pos+1] != 'u' {
		return 0, false
	}
	lo, ok := hex4(d.s[d.pos+2 : d.pos+6])
	if !ok || lo < 0xDC00 || lo >= 0xE000 {
		return 0, false
	}
	d.pos += 6
	return utf16.DecodeRune(hi, lo), true
}

// isNumber reports whether tok is a decimal number: -? digits ("." digits?)?
// or -? "." digits, with an optional exponent. Underscores, hex and
// Inf/NaN words are strings.
func isNumber(tok string) bool {
	i := 0
	if i < len(tok) && tok[i] == '-' {
		i++
	}
	digits := func() int {
		s := i
		for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
			i++
		}
		return i - s
	}
	intDigits := digits()
	frac := 0
	if i < len(tok) && tok[i] == '.' {
		i++
		frac = digits()
	}
	if intDigits == 0 && frac == 0 {
		return false
	}
	if i < len(tok) && (tok[i] == 'e' || tok[i] == 'E') {
		i++
		if i < len(tok) && (tok[i] == '+' || tok[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == len(tok)
}
