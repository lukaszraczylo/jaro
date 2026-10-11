package jaro

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
)

// Column kinds. A column is n values stored together so general-purpose
// compressors see runs of similar bytes.
const (
	cGeneric byte = iota // n values, each a full tagged value
	cDelta               // all ints: zigzag varint of difference from the previous
	cDecimal             // all numbers: k, then zigzag varint of value*10^k
	cDict                // all strings: dict size, dict strings, varint index per value
	cBool                // all bools: bitset
	cObj                 // all objects with equal keys: key count, keys, one column per key
)

const (
	tCol        = 0x0C // uvarint n, column
	maxDecimals = 6
	minColumn   = 4 // arrays shorter than this stay plain
	maxExact    = 1 << 53
)

var pow10 = [...]float64{1, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15}

type colPlan struct {
	dict []string
	idx  map[string]uint64
	keys []string
	cols [][]any // cObj: values per key
	k    int
	kind byte
}

func zigzagLen(v int64) int {
	u := uint64(v<<1) ^ uint64(v>>63) //nolint:gosec // zigzag needs the bit pattern
	n := 1
	for u >= 0x80 {
		u >>= 7
		n++
	}
	return n
}

// classify picks the cheapest lossless column kind for vals.
func classify(vals []any) colPlan {
	n := len(vals)
	if n == 0 {
		return colPlan{kind: cGeneric}
	}
	switch vals[0].(type) {
	case bool:
		for _, v := range vals {
			if _, ok := v.(bool); !ok {
				return colPlan{kind: cGeneric}
			}
		}
		return colPlan{kind: cBool}
	case string:
		return classifyStrings(vals)
	case map[string]any:
		keys, cols, ok := tableData(vals)
		if !ok && n == 1 {
			one := vals[0].(map[string]any)
			if len(one) > 0 {
				keys = sortedKeys(one)
				cols = make([][]any, len(keys))
				for j, k := range keys {
					cols[j] = []any{one[k]}
				}
				ok = true
			}
		}
		if ok {
			return colPlan{kind: cObj, keys: keys, cols: cols}
		}
		return colPlan{kind: cGeneric}
	}
	return classifyNumbers(vals)
}

func classifyStrings(vals []any) colPlan {
	idx := make(map[string]uint64)
	var dict []string
	for _, v := range vals {
		s, ok := v.(string)
		if !ok {
			return colPlan{kind: cGeneric}
		}
		if _, seen := idx[s]; !seen {
			idx[s] = uint64(len(dict))
			dict = append(dict, s)
			if len(dict)*2 > len(vals) { // distinct count only grows: not dictionary-worthy
				return colPlan{kind: cGeneric}
			}
		}
	}
	if len(vals) >= minColumn && len(dict)*2 <= len(vals) {
		return colPlan{kind: cDict, dict: dict, idx: idx}
	}
	return colPlan{kind: cGeneric}
}

func classifyNumbers(vals []any) colPlan {
	nums := make([]num, len(vals))
	allInt := true
	k := 0
	for i, v := range vals {
		n, ok, err := normNum(v)
		if !ok || err != nil {
			return colPlan{kind: cGeneric}
		}
		nums[i] = n
		if n.isInt {
			continue
		}
		allInt = false
		kk := decimals(n.f)
		if kk < 0 {
			return colPlan{kind: cGeneric}
		}
		k = max(k, kk)
	}
	if allInt {
		plain, delta := 0, 0
		prev := int64(0)
		for _, n := range nums {
			plain += zigzagLen(n.i)
			delta += zigzagLen(n.i - prev)
			prev = n.i
		}
		if delta < plain {
			return colPlan{kind: cDelta}
		}
		return colPlan{kind: cDecimal}
	}
	for _, n := range nums {
		if n.isInt {
			if math.Abs(float64(n.i))*pow10[k] >= maxExact {
				return colPlan{kind: cGeneric}
			}
			continue
		}
		// The shared scale can push a value past 2^53 or off its exact decimal.
		if m := math.Round(n.f * pow10[k]); math.Abs(m) >= maxExact || m/pow10[k] != n.f {
			return colPlan{kind: cGeneric}
		}
	}
	return colPlan{kind: cDecimal, k: k}
}

// decimals returns the fewest decimal places (<= maxDecimals) that represent
// f exactly as mantissa/10^k, or -1.
func decimals(f float64) int {
	for k := 0; k <= maxDecimals; k++ {
		m := math.Round(f * pow10[k])
		if math.Abs(m) < maxExact && m/pow10[k] == f {
			return k
		}
	}
	return -1
}

func (e *binEnc) column(vals []any, depth int) error {
	if depth > maxDepth {
		return errors.New("jaro: nesting too deep")
	}
	p := classify(vals)
	e.buf = append(e.buf, p.kind)
	switch p.kind {
	case cGeneric:
		for _, v := range vals {
			if err := e.value(v, depth+1); err != nil {
				return err
			}
		}
	case cDelta:
		prev := int64(0)
		for _, v := range vals {
			n, _, _ := normNum(v)
			e.buf = binary.AppendVarint(e.buf, n.i-prev)
			prev = n.i
		}
	case cDecimal:
		e.buf = append(e.buf, byte(p.k&7))
		for _, v := range vals {
			n, _, _ := normNum(v)
			if p.k == 0 { // only ints reach k=0; keep them exact beyond 2^53
				e.buf = binary.AppendVarint(e.buf, n.i)
				continue
			}
			f := n.f
			if n.isInt {
				f = float64(n.i)
			}
			e.buf = binary.AppendVarint(e.buf, int64(math.Round(f*pow10[p.k])))
		}
	case cDict:
		e.uvarint(uint64(len(p.dict)))
		for _, s := range p.dict {
			e.str(s)
		}
		for _, v := range vals {
			e.uvarint(p.idx[v.(string)])
		}
	case cBool:
		bits := make([]byte, (len(vals)+7)/8)
		for i, v := range vals {
			if v.(bool) {
				bits[i/8] |= 1 << (i % 8)
			}
		}
		e.buf = append(e.buf, bits...)
	case cObj:
		e.uvarint(uint64(len(p.keys)))
		for _, k := range p.keys {
			e.str(k)
		}
		for _, col := range p.cols {
			if err := e.column(col, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *binDec) column(n, depth int) ([]any, error) {
	if depth > maxDepth {
		return nil, errors.New("jaro: nesting too deep")
	}
	if err := d.charge(n + 1); err != nil {
		return nil, err
	}
	if d.pos >= len(d.s) {
		return nil, errShort
	}
	kind := d.s[d.pos]
	d.pos++
	out := make([]any, n)
	var err error
	switch kind {
	case cGeneric:
		for i := range out {
			if out[i], err = d.value(depth + 1); err != nil {
				return nil, err
			}
		}
	case cDelta:
		prev := int64(0)
		for i := range out {
			v, w := binary.Varint(d.s[d.pos:])
			if w <= 0 {
				return nil, errShort
			}
			d.pos += w
			prev += v
			out[i] = d.box.int(prev)
		}
	case cDecimal:
		if d.pos >= len(d.s) || int(d.s[d.pos]) > maxDecimals {
			return nil, errors.New("jaro: bad decimal scale")
		}
		k := int(d.s[d.pos])
		d.pos++
		for i := range out {
			v, w := binary.Varint(d.s[d.pos:])
			if w <= 0 {
				return nil, errShort
			}
			d.pos += w
			out[i] = d.decimalValue(v, k)
		}
	case cDict:
		nd, err := d.count()
		if err != nil {
			return nil, err
		}
		dict := make([]any, nd) // boxed once, shared by every cell
		for i := range dict {
			s, err := d.key()
			if err != nil {
				return nil, err
			}
			dict[i] = d.box.str(s)
		}
		for i := range out {
			idx, err := d.uvarint()
			if err != nil {
				return nil, err
			}
			if idx >= uint64(len(dict)) {
				return nil, errors.New("jaro: bad dictionary index")
			}
			out[i] = dict[idx]
		}
	case cBool:
		nb := (n + 7) / 8
		if len(d.s)-d.pos < nb {
			return nil, errShort
		}
		for i := range out {
			out[i] = d.s[d.pos+i/8]>>(i%8)&1 == 1
		}
		d.pos += nb
	case cObj:
		return d.objColumn(n, depth)
	default:
		return nil, errors.New("jaro: bad column kind")
	}
	return out, nil
}

func (d *binDec) objColumn(n, depth int) ([]any, error) {
	nk, err := d.count()
	if err != nil {
		return nil, err
	}
	if nk == 0 {
		return nil, errors.New("jaro: empty object column")
	}
	keys := make([]string, nk)
	for i := range keys {
		if keys[i], err = d.key(); err != nil {
			return nil, err
		}
	}
	if k, dup := duplicate(keys); dup {
		return nil, fmt.Errorf("jaro: duplicate object column key %q", k)
	}
	maps := make([]map[string]any, n)
	out := make([]any, n)
	for i := range maps {
		maps[i] = make(map[string]any, nk)
		out[i] = maps[i]
	}
	for _, k := range keys {
		col, err := d.column(n, depth+1)
		if err != nil {
			return nil, err
		}
		for i, m := range maps {
			m[k] = col[i]
		}
	}
	return out, nil
}

// decimalValue returns m/10^k as int64 when integral, else float64.
func (d *binDec) decimalValue(m int64, k int) any {
	if k == 0 {
		return d.box.int(m)
	}
	f := float64(m) / pow10[k]
	if f == math.Trunc(f) && math.Abs(f) < maxExact {
		return d.box.int(int64(f))
	}
	return d.box.float(f)
}

const boxedInts = 1024

var (
	boxOnce  sync.Once
	boxTable [boxedInts]any
)

// boxInt boxes i, sharing one interface value for 0 <= i < boxedInts so
// common small integers (ids, counts, ports) cost no allocation. The table
// is built on first use and never written afterwards.
func boxInt(i int64) any {
	if i < 0 || i >= boxedInts {
		return i
	}
	boxOnce.Do(func() {
		for n := range boxTable {
			boxTable[n] = int64(n)
		}
	})
	return boxTable[i]
}
