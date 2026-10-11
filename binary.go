package jaro

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	mbits "math/bits"
	"strings"
	"unicode/utf8"
)

const (
	tNull     = 0x00
	tFalse    = 0x01
	tTrue     = 0x02
	tInt      = 0x03
	tFloat    = 0x04
	tStr      = 0x05
	tRef      = 0x06
	tArr      = 0x07
	tObj      = 0x08
	tTable    = 0x09
	tAnchor   = 0x0A // next value defines the next anchor index
	tAnchRef  = 0x0B // uvarint anchor index
	tSparse   = 0x0D // table whose rows may lack keys: columns, keys, rows, then per column a presence flag, optional bitmap and the present values
	tShortStr = 0x40 // 0x40|len, len < 32
	tSmallInt = 0x80 // 0x80|n, n < 128

	binMagic   = 0x0F // first byte of every JARO binary document
	binVersion = 0x01

	minPooled = 2 // strings shorter than this are never pooled
)

// MarshalBinary encodes v as JARO binary. Repeated strings (including keys)
// become 1-2 byte back-references; arrays of same-shape objects are stored
// column-wise.
func MarshalBinary(v any) ([]byte, error) { return MarshalBinaryOpts(v, Options{}) }

// MarshalBinaryOpts is MarshalBinary with options.
func MarshalBinaryOpts(v any, o Options) ([]byte, error) {
	e := &binEnc{buf: getBuf(), pool: getPool()}
	defer func() { putBuf(e.buf); putPool(e.pool) }()
	if o.Dedup {
		e.dd = newDedup(v, o.minDedup(), true)
	}
	e.buf = append(e.buf, binMagic, binVersion)
	if err := e.value(v, 0); err != nil {
		return nil, err
	}
	return bytes.Clone(e.buf), nil
}

type binEnc struct {
	pool map[string]uint64
	dd   *dedup
	buf  []byte
	ks   []string // scratch stack of sorted object keys
}

func (e *binEnc) uvarint(n uint64) { e.buf = binary.AppendUvarint(e.buf, n) }

func (e *binEnc) str(s string) {
	pooled := len(s) >= minPooled
	if pooled && e.ref(s) {
		return
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
		if pooled = len(s) >= minPooled; pooled && e.ref(s) {
			return
		}
	}
	if pooled {
		e.pool[s] = uint64(len(e.pool))
	}
	if len(s) < 32 {
		e.buf = append(e.buf, tShortStr|byte(len(s)&31))
	} else {
		e.buf = append(e.buf, tStr)
		e.uvarint(uint64(len(s)))
	}
	e.buf = append(e.buf, s...)
}

// ref writes a back-reference to s and reports true when s is already pooled.
func (e *binEnc) ref(s string) bool {
	idx, ok := e.pool[s]
	if ok {
		e.buf = append(e.buf, tRef)
		e.uvarint(idx)
	}
	return ok
}

func (e *binEnc) value(v any, depth int) error {
	if depth > maxDepth {
		return errors.New("jaro: nesting too deep")
	}
	if e.dd != nil {
		if idx, use, def := e.dd.ref(v); use {
			e.buf = append(e.buf, tAnchRef)
			e.uvarint(idx)
			return nil
		} else if def {
			e.buf = append(e.buf, tAnchor)
		}
	}
	switch x := v.(type) {
	case nil:
		e.buf = append(e.buf, tNull)
	case bool:
		if x {
			e.buf = append(e.buf, tTrue)
		} else {
			e.buf = append(e.buf, tFalse)
		}
	case string:
		e.str(x)
	case map[string]any:
		e.buf = append(e.buf, tObj)
		e.uvarint(uint64(len(x)))
		start := len(e.ks)
		e.ks = appendSortedKeys(e.ks, x)
		for _, k := range e.ks[start:] {
			e.str(k)
			if err := e.value(x[k], depth+1); err != nil {
				return err
			}
		}
		e.ks = e.ks[:start]
	case []any:
		if keys, cols, ok := tableData(x); ok {
			e.buf = append(e.buf, tTable)
			e.uvarint(uint64(len(keys)))
			for _, k := range keys {
				e.str(k)
			}
			e.uvarint(uint64(len(x)))
			for _, col := range cols {
				if err := e.column(col, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		if keys, ok := sparseKeys(x, binPresent); ok {
			return e.sparse(x, keys, depth)
		}
		if len(x) >= minColumn {
			if p := classify(x); p.kind != cGeneric {
				e.buf = append(e.buf, tCol)
				e.uvarint(uint64(len(x)))
				return e.column(x, depth+1)
			}
		}
		e.buf = append(e.buf, tArr)
		e.uvarint(uint64(len(x)))
		for _, el := range x {
			if err := e.value(el, depth+1); err != nil {
				return err
			}
		}
	default:
		n, ok, err := normNum(v)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("jaro: unsupported type %T", v)
		}
		switch {
		case n.isInt && n.i >= 0 && n.i < 128:
			e.buf = append(e.buf, tSmallInt|byte(n.i))
		case n.isInt:
			e.buf = append(e.buf, tInt)
			e.buf = binary.AppendVarint(e.buf, n.i)
		default:
			e.buf = append(e.buf, tFloat)
			e.buf = binary.LittleEndian.AppendUint64(e.buf, math.Float64bits(n.f))
		}
	}
	return nil
}

// UnmarshalBinary decodes JARO binary produced by MarshalBinary.
func UnmarshalBinary(data []byte) (any, error) {
	if len(data) < 2 || data[0] != binMagic {
		return nil, errors.New("jaro: not a JARO binary document")
	}
	if data[1] != binVersion {
		return nil, fmt.Errorf("jaro: unsupported binary version %d", data[1])
	}
	d := &binDec{s: data, str: string(data), pos: 2, budget: maxExpand, nodes: 1<<22 + 32*len(data)}
	v, err := d.value(0)
	if err != nil {
		return nil, err
	}
	if d.pos != len(d.s) {
		return nil, errors.New("jaro: trailing data")
	}
	return v, nil
}

type binDec struct {
	cellArena
	box     slab
	str     string
	s       []byte
	pool    []string
	anchors []any
	filled  []bool
	pos     int
	budget  int
	nodes   int
}

var errShort = errors.New("jaro: truncated input")

func (d *binDec) uvarint() (uint64, error) {
	n, w := binary.Uvarint(d.s[d.pos:])
	if w <= 0 {
		return 0, errShort
	}
	d.pos += w
	return n, nil
}

// count reads an element count and rejects values that exceed the bytes left,
// so hostile input cannot force a huge allocation.
func (d *binDec) count() (int, error) {
	n, err := d.uvarint()
	if err != nil {
		return 0, err
	}
	if n > math.MaxInt32 || int(n) > len(d.s)-d.pos {
		return 0, errShort
	}
	return int(n), nil
}

// rowCount reads a row/element count for column data. Bool columns take one
// bit per value, so the bound is 8x the bytes left; charge() limits the memory
// that bound can force.
func (d *binDec) rowCount() (int, error) {
	n, err := d.uvarint()
	if err != nil {
		return 0, err
	}
	if n > math.MaxInt32 || int(n) > (len(d.s)-d.pos)*8 {
		return 0, errShort
	}
	return int(n), nil
}

func (d *binDec) charge(n int) error {
	if d.nodes -= n; d.nodes < 0 {
		return errors.New("jaro: decoded size too large")
	}
	return nil
}

func (d *binDec) strBytes(n int) (string, error) {
	if n > len(d.s)-d.pos {
		return "", errShort
	}
	s := d.str[d.pos : d.pos+n] // substring of the one input copy
	d.pos += n
	if !utf8.ValidString(s) {
		return "", errors.New("jaro: string is not valid UTF-8")
	}
	if n >= minPooled {
		d.pool = append(d.pool, s)
	}
	return s, nil
}

// key reads a string value used as an object key or table column.
func (d *binDec) key() (string, error) {
	if d.pos < len(d.s) {
		switch t := d.s[d.pos]; {
		case t >= tShortStr && t < tShortStr+32:
			d.pos++
			return d.strBytes(int(t & 31))
		case t == tStr:
			d.pos++
			n, err := d.count()
			if err != nil {
				return "", err
			}
			return d.strBytes(n)
		case t == tRef:
			d.pos++
			idx, err := d.uvarint()
			if err != nil {
				return "", err
			}
			if idx >= uint64(len(d.pool)) {
				return "", errors.New("jaro: bad string reference")
			}
			return d.pool[idx], nil
		}
	}
	v, err := d.value(maxDepth)
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", errors.New("jaro: key must be a string")
	}
	return s, nil
}

func (d *binDec) value(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("jaro: nesting too deep")
	}
	if d.pos >= len(d.s) {
		return nil, errShort
	}
	t := d.s[d.pos]
	d.pos++
	switch {
	case t >= tSmallInt:
		return int64(t & 0x7f), nil
	case t >= tShortStr && t < tShortStr+32:
		s, err := d.strBytes(int(t & 31))
		if err != nil {
			return nil, err
		}
		return d.box.str(s), nil
	}
	switch t {
	case tNull:
		return nil, nil
	case tFalse:
		return false, nil
	case tTrue:
		return true, nil
	case tInt:
		n, w := binary.Varint(d.s[d.pos:])
		if w <= 0 {
			return nil, errShort
		}
		d.pos += w
		return d.box.int(n), nil
	case tFloat:
		if len(d.s)-d.pos < 8 {
			return nil, errShort
		}
		f := math.Float64frombits(binary.LittleEndian.Uint64(d.s[d.pos:]))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("jaro: non-finite float")
		}
		d.pos += 8
		return d.box.float(f), nil
	case tStr:
		n, err := d.count()
		if err != nil {
			return nil, err
		}
		s, err := d.strBytes(n)
		if err != nil {
			return nil, err
		}
		return d.box.str(s), nil
	case tRef:
		idx, err := d.uvarint()
		if err != nil {
			return nil, err
		}
		if idx >= uint64(len(d.pool)) {
			return nil, errors.New("jaro: bad string reference")
		}
		return d.pool[idx], nil
	case tArr:
		n, err := d.count()
		if err != nil {
			return nil, err
		}
		out := d.cells(n)
		for i := range out {
			if out[i], err = d.value(depth + 1); err != nil {
				return nil, err
			}
		}
		return out, nil
	case tObj:
		n, err := d.count()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, n)
		for i := 0; i < n; i++ {
			k, err := d.key()
			if err != nil {
				return nil, err
			}
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			before := len(m)
			m[k] = v
			if len(m) == before {
				return nil, fmt.Errorf("jaro: duplicate key %q", k)
			}
		}
		return m, nil
	case tTable:
		return d.table(depth)
	case tSparse:
		return d.sparse(depth)
	case tCol:
		n, err := d.rowCount()
		if err != nil {
			return nil, err
		}
		return d.column(n, depth+1)
	case tAnchor:
		idx := len(d.anchors)
		d.anchors = append(d.anchors, nil)
		d.filled = append(d.filled, false)
		v, err := d.value(depth + 1)
		if err != nil {
			return nil, err
		}
		d.anchors[idx], d.filled[idx] = v, true
		return v, nil
	case tAnchRef:
		idx, err := d.uvarint()
		if err != nil {
			return nil, err
		}
		if idx >= uint64(len(d.anchors)) || !d.filled[idx] {
			return nil, errors.New("jaro: bad anchor reference")
		}
		return deepCopy(d.anchors[idx], &d.budget)
	}
	return nil, fmt.Errorf("jaro: bad tag 0x%02x at %d", t, d.pos-1)
}

// sparse writes a table whose rows may lack keys. Each column carries a
// presence flag (1 = bitmap follows) and only the values that exist.
func (e *binEnc) sparse(rows []any, keys []string, depth int) error {
	e.buf = append(e.buf, tSparse)
	e.uvarint(uint64(len(keys)))
	for _, k := range keys {
		e.str(k)
	}
	e.uvarint(uint64(len(rows)))
	for _, k := range keys {
		vals := make([]any, 0, len(rows))
		bits := make([]byte, (len(rows)+7)/8)
		for i, r := range rows {
			if v, ok := r.(map[string]any)[k]; ok {
				vals = append(vals, v)
				bits[i/8] |= 1 << (i % 8)
			}
		}
		if len(vals) == len(rows) {
			e.buf = append(e.buf, 0)
		} else {
			e.buf = append(e.buf, 1)
			e.buf = append(e.buf, bits...)
		}
		if err := e.column(vals, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (d *binDec) sparse(depth int) (any, error) {
	nc, err := d.count()
	if err != nil {
		return nil, err
	}
	if nc == 0 {
		return nil, errors.New("jaro: empty table header")
	}
	cols := make([]string, nc)
	for i := range cols {
		if cols[i], err = d.key(); err != nil {
			return nil, err
		}
	}
	if k, dup := duplicate(cols); dup {
		return nil, fmt.Errorf("jaro: duplicate table key %q", k)
	}
	nr, err := d.rowCount()
	if err != nil {
		return nil, err
	}
	if err = d.charge(nr * 2); err != nil {
		return nil, err
	}
	maps := make([]map[string]any, nr)
	rows := make([]any, nr)
	for i := range maps {
		maps[i] = make(map[string]any, nc)
		rows[i] = maps[i]
	}
	for _, k := range cols {
		if d.pos >= len(d.s) {
			return nil, errShort
		}
		flag := d.s[d.pos]
		d.pos++
		var bits []byte
		n := nr
		switch flag {
		case 0:
		case 1:
			nb := (nr + 7) / 8
			if len(d.s)-d.pos < nb {
				return nil, errShort
			}
			bits = d.s[d.pos : d.pos+nb]
			d.pos += nb
			n = 0
			for _, by := range bits {
				n += mbits.OnesCount8(by)
			}
			if n > nr || (nr%8 != 0 && bits[nb-1]>>(nr%8) != 0) {
				return nil, errors.New("jaro: bad presence bitmap")
			}
		default:
			return nil, errors.New("jaro: bad presence flag")
		}
		col, err := d.column(n, depth+1)
		if err != nil {
			return nil, err
		}
		j := 0
		for i, m := range maps {
			if bits != nil && bits[i/8]>>(i%8)&1 == 0 {
				continue
			}
			m[k] = col[j]
			j++
		}
	}
	return rows, nil
}

func (d *binDec) table(depth int) (any, error) {
	nc, err := d.count()
	if err != nil {
		return nil, err
	}
	if nc == 0 {
		return nil, errors.New("jaro: empty table header")
	}
	cols := make([]string, nc)
	for i := range cols {
		if cols[i], err = d.key(); err != nil {
			return nil, err
		}
	}
	if k, dup := duplicate(cols); dup {
		return nil, fmt.Errorf("jaro: duplicate table key %q", k)
	}
	nr, err := d.rowCount()
	if err != nil {
		return nil, err
	}
	maps := make([]map[string]any, nr)
	rows := make([]any, nr)
	for i := range maps {
		maps[i] = make(map[string]any, nc)
		rows[i] = maps[i]
	}
	for _, k := range cols {
		col, err := d.column(nr, depth+1)
		if err != nil {
			return nil, err
		}
		for i, m := range maps {
			m[k] = col[i]
		}
	}
	return rows, nil
}
