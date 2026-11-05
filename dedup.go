package jaro

import (
	"errors"
	"math"
	"math/bits"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	defaultMinDedup = 8
	maxExpand       = 1 << 24 // nodes a decoder may materialise through references
)

// Options tunes encoding.
type Options struct {
	// Dedup replaces repeated sub-trees with anchors and back-references.
	Dedup bool
	// MinDedup is the smallest sub-tree (in text bytes) worth anchoring.
	// Strings are anchored by their estimated token cost instead.
	MinDedup int
}

func (o Options) minDedup() int {
	if o.MinDedup > 0 {
		return o.MinDedup
	}
	return defaultMinDedup
}

// dedup holds the anchor plan. Its traversal mirrors the encoder's emission
// order so the first emitted copy of a repeated sub-tree is the definition.
//
// Equal sub-trees get one class id in a single bottom-up pass (hash-consing),
// so no container is serialised per ancestor. A class is only serialised, and
// only up to min bytes, when it occurs at least twice.
// lean4/Jaro/DedupPlan.lean models this planner.
type dedup struct {
	strN       map[string]int32
	byHash     map[uint64]uint32
	strs       map[string]uint32
	strIdx     map[string]uint64
	ids        map[nodeID]uint32
	sigs       []elem
	ks         []string
	vks        []string
	counts     []int32
	names      []int32
	stk        []elem
	cls        []class
	enc        textEnc
	nnames     uint64
	poison     uint64
	min        int
	anchorStrs bool
	col        bool
}

// planner decides, per container at emission time, where anchors go.
type planner interface {
	ref(v any) (idx uint64, use, def bool)
	refStr(s string) (idx uint64, use, def bool)
}

// nodeID identifies a container by address and length. The tree is not
// mutated while the plan is built, so the class of a node never changes.
type nodeID struct {
	ptr uintptr
	n   int
}

type class struct {
	rep   any // first container of the class
	off   uint32
	n     uint32
	next  uint32 // previous class+1 with the same hash
	freq  int32  // occurrences in the document
	elig  int8   // 0 unknown, 1 text >= min bytes, 2 shorter
	array bool
}

// elem is one child in a signature: a scalar value or the class of a container.
type elem struct {
	v uint64
	t uint8
}

const (
	eNil uint8 = iota
	eFalse
	eTrue
	eInt
	eFloat
	eStr
	eArr
	eMap
	eMarkArr
	eMarkMap
	ePoison
)

const sigMix = 0x9E3779B97F4A7C15

var dedupPool = sync.Pool{New: func() any {
	return &dedup{
		ids: map[nodeID]uint32{}, byHash: map[uint64]uint32{}, strs: map[string]uint32{},
		strN: map[string]int32{}, strIdx: map[string]uint64{},
	}
}}

// maxPooledClasses keeps one huge document from pinning memory in the pool.
const maxPooledClasses = 1 << 18

// newDedup plans the anchors of v. Call release when the encoder is done.
func newDedup(v any, min int, columnMajor bool) *dedup {
	d := dedupPool.Get().(*dedup)
	d.min, d.col, d.anchorStrs = min, columnMajor, !columnMajor
	d.walk(v)
	n := len(d.cls)
	d.counts = slices.Grow(d.counts[:0], n)[:n]
	d.names = slices.Grow(d.names[:0], n)[:n]
	clear(d.counts)
	clear(d.names)
	d.visit(v)
	return d
}

// release returns d to the pool. It drops every reference into the document.
func (d *dedup) release() {
	if len(d.cls) > maxPooledClasses {
		return
	}
	clear(d.ids)
	clear(d.byHash)
	clear(d.strs)
	clear(d.strN)
	clear(d.strIdx)
	clear(d.cls)
	d.cls, d.sigs, d.stk, d.ks, d.vks = d.cls[:0], d.sigs[:0], d.stk[:0], d.ks[:0], d.vks[:0]
	clear(d.enc.ks)
	d.enc.ks, d.enc.buf = d.enc.ks[:0], d.enc.buf[:0]
	d.nnames, d.poison = 0, 0
	dedupPool.Put(d)
}

// strID interns s as the encoder writes it: invalid UTF-8 becomes U+FFFD, so
// two strings with equal text share an id.
func (d *dedup) strID(s string) uint64 {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	id, ok := d.strs[s]
	if !ok {
		id = uint32(len(d.strs)) //nolint:gosec // bounded by input size
		d.strs[s] = id
	}
	return uint64(id)
}

// walk returns the element of v and records the class of every container
// below it. It visits every occurrence, so class frequencies count each copy.
func (d *dedup) walk(v any) elem {
	switch x := v.(type) {
	case nil:
		return elem{t: eNil}
	case bool:
		if x {
			return elem{t: eTrue}
		}
		return elem{t: eFalse}
	case string:
		return elem{d.strID(x), eStr}
	case map[string]any:
		base, kb := len(d.stk), len(d.ks)
		d.stk = append(d.stk, elem{t: eMarkMap})
		d.ks = appendSortedKeys(d.ks, x)
		keys := d.ks[kb:]
		for _, k := range keys {
			c := d.walk(x[k])
			d.stk = append(d.stk, elem{d.strID(k), eStr}, c)
		}
		d.ks = d.ks[:kb]
		return d.seal(v, base, len(x), false)
	case []any:
		base := len(d.stk)
		d.stk = append(d.stk, elem{t: eMarkArr})
		for _, el := range x {
			c := d.walk(el)
			d.stk = append(d.stk, c)
		}
		return d.seal(v, base, len(x), true)
	}
	n, ok, err := normNum(v)
	switch {
	case !ok || err != nil: // the encoder rejects it, so the document fails anyway
		d.poison++
		return elem{d.poison, ePoison}
	case n.isInt:
		return elem{uint64(n.i), eInt} //nolint:gosec // bit pattern
	}
	return elem{math.Float64bits(n.f), eFloat}
}

// seal turns the signature stacked above base into a class.
func (d *dedup) seal(v any, base, n int, array bool) elem {
	sg := d.stk[base:]
	h := uint64(len(sg))
	for _, e := range sg {
		h = (bits.RotateLeft64(h, 23) ^ e.v ^ uint64(e.t)<<56) * sigMix
	}
	c, found := d.find(h, sg)
	if !found {
		c = uint32(len(d.cls))                                                                                              //nolint:gosec // bounded by input size
		d.cls = append(d.cls, class{rep: v, off: uint32(len(d.sigs)), n: uint32(len(sg)), next: d.byHash[h], array: array}) //nolint:gosec // bounded by input size
		d.sigs = append(d.sigs, sg...)
		d.byHash[h] = c + 1
	}
	d.stk = d.stk[:base]
	d.cls[c].freq++
	if n > 0 {
		d.ids[d.nodeID(v)] = c
	}
	if array {
		return elem{uint64(c), eArr}
	}
	return elem{uint64(c), eMap}
}

// find returns the class whose signature equals sg. Every hash hit compares
// the stored signature, so a collision cannot merge unequal sub-trees.
func (d *dedup) find(h uint64, sg []elem) (uint32, bool) {
	for p := d.byHash[h]; p != 0; p = d.cls[p-1].next {
		cl := &d.cls[p-1]
		if slices.Equal(d.sigs[cl.off:cl.off+cl.n], sg) {
			return p - 1, true
		}
	}
	return 0, false
}

func (d *dedup) nodeID(v any) nodeID {
	switch x := v.(type) {
	case map[string]any:
		return nodeID{reflect.ValueOf(v).Pointer(), len(x)} // v is already boxed: no allocation
	case []any:
		return nodeID{reflect.ValueOf(v).Pointer(), len(x)}
	}
	return nodeID{}
}

// classOf returns the class of container v.
func (d *dedup) classOf(v any) (uint32, bool) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			return d.empty(eMarkMap, false)
		}
	case []any:
		if len(x) == 0 {
			return d.empty(eMarkArr, true)
		}
	default:
		return 0, false
	}
	c, ok := d.ids[d.nodeID(v)]
	return c, ok
}

// empty returns the class of {} or [], which walk does not register by address.
func (d *dedup) empty(mark uint8, array bool) (uint32, bool) {
	sg := []elem{{t: mark}}
	h := uint64(1)
	h = (bits.RotateLeft64(h, 23) ^ uint64(mark)<<56) * sigMix
	return d.find(h, sg)
}

// counted returns the class of v when v can be anchored: it occurs twice or
// more and its text has at least min bytes.
func (d *dedup) counted(v any) (uint32, bool) {
	c, ok := d.classOf(v)
	if !ok || d.cls[c].freq < 2 {
		return 0, false
	}
	cl := &d.cls[c]
	if cl.elig == 0 {
		cl.elig = 2
		if d.longEnough(cl.rep) {
			cl.elig = 1
		}
	}
	return c, cl.elig == 1
}

// longEnough reports whether the text of v has at least d.min bytes. It stops
// serialising at d.min bytes: the buffer only grows (cap_sound in the model).
func (d *dedup) longEnough(v any) bool {
	e := &d.enc
	e.buf, e.ks, e.limit = e.buf[:0], e.ks[:0], d.min
	var err error
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		err = e.objBody(m, 1)
	} else {
		err = e.value(v, 0)
	}
	return len(e.buf) >= d.min && (err == nil || err == errEnough)
}

// Anchoring a string replaces each later copy (c tokens) with a reference of two
// tokens and adds a definition of two tokens (` &`, index). It pays when
// (n-1)(c-2) > 2: Jaro.Dedup.tok_pays_iff in lean4/.
const (
	minAnchorStr = 3 // shorter strings cost under 3 tokens even when quoted
	refTokens    = 2
	defTokens    = 2
)

// tokCost estimates the LLM tokens of s as the text encoder writes it, for
// o200k_base: an ASCII word costs a token per 6 letters, digits a token per 3,
// other characters one each, and a quoted string two more for its quotes.
func tokCost(s string) int {
	c := 1
	if needsQuote(s) {
		c++
	}
	for i := 0; i < len(s); {
		b := s[i]
		switch {
		case b >= utf8.RuneSelf:
			_, n := utf8.DecodeRuneInString(s[i:])
			c++
			i += n
		case b >= '0' && b <= '9':
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			c += (j - i + 2) / 3
			i = j
		case b|0x20 >= 'a' && b|0x20 <= 'z':
			j := i
			for j < len(s) && s[j]|0x20 >= 'a' && s[j]|0x20 <= 'z' {
				j++
			}
			c += (j - i + 5) / 6
			i = j
		default:
			c++
			i++
		}
	}
	return c
}

func sanitized(s string) string {
	if !utf8.ValidString(s) {
		return strings.ToValidUTF8(s, "\uFFFD")
	}
	return s
}

// refStr is called once per string value at emission time, like ref.
func (d *dedup) refStr(s string) (idx uint64, use, def bool) {
	if !d.anchorStrs || len(s) < minAnchorStr {
		return 0, false, false
	}
	k := sanitized(s)
	n := int(d.strN[k])
	if n < 2 || (n-1)*(tokCost(k)-refTokens) <= defTokens {
		return 0, false, false
	}
	if i, seen := d.strIdx[k]; seen {
		return i, true, false
	}
	i := d.nnames
	d.nnames++
	d.strIdx[k] = i
	return i, false, true
}

func (d *dedup) visit(v any) {
	if s, ok := v.(string); ok {
		if d.anchorStrs && len(s) >= minAnchorStr {
			d.strN[sanitized(s)]++
		}
		return
	}
	if c, ok := d.counted(v); ok {
		d.counts[c]++
		if d.counts[c] > 1 {
			return
		}
	}
	switch x := v.(type) {
	case map[string]any:
		if !d.col {
			if specs, ok := keyedSpecs(x); ok { // the text encoder writes row cells, not row objects
				base := len(d.vks)
				d.vks = appendSortedKeys(d.vks, x)
				for _, name := range d.vks[base:] {
					d.visitCells(specs, x[name].(map[string]any))
				}
				d.vks = d.vks[:base]
				return
			}
		}
		base := len(d.vks)
		d.vks = appendSortedKeys(d.vks, x)
		for _, k := range d.vks[base:] {
			d.visit(x[k])
		}
		d.vks = d.vks[:base]
	case []any:
		if !d.col {
			if specs, ok := tableSpecs(x); ok {
				for _, r := range x {
					d.visitCells(specs, r.(map[string]any))
				}
				return
			}
		}
		cols, ok := tableKeys(x)
		switch {
		case !ok:
			for _, el := range x {
				d.visit(el)
			}
		case d.col:
			for _, c := range cols {
				for _, row := range x {
					d.visit(row.(map[string]any)[c])
				}
			}
		default:
			for _, row := range x {
				for _, c := range cols {
					d.visit(row.(map[string]any)[c])
				}
			}
		}
	}
}

// visitCells visits the cells of one table row in the order specCells writes them.
func (d *dedup) visitCells(specs []colSpec, row map[string]any) {
	for _, s := range specs {
		v, present := row[s.key]
		switch {
		case s.sub != nil:
			d.visitCells(s.sub, v.(map[string]any))
		case present:
			d.visit(v)
		}
	}
}

// ref is called once per container at emission time. use: emit a reference to
// idx. def: emit a definition with index idx, then the value.
func (d *dedup) ref(v any) (idx uint64, use, def bool) {
	c, ok := d.counted(v)
	if !ok || d.counts[c] < 2 {
		return 0, false, false
	}
	if n := d.names[c]; n != 0 {
		return uint64(n - 1), true, false //nolint:gosec // n >= 1 here
	}
	i := d.nnames
	d.nnames++
	d.names[c] = int32(i) + 1 //nolint:gosec // bounded by container count
	return i, false, true
}

func deepCopy(v any, budget *int) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		if *budget -= len(x) + 1; *budget < 0 {
			return nil, errors.New("jaro: reference expansion too large")
		}
		out := make(map[string]any, len(x))
		for k, el := range x {
			c, err := deepCopy(el, budget)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case []any:
		if *budget -= len(x) + 1; *budget < 0 {
			return nil, errors.New("jaro: reference expansion too large")
		}
		out := make([]any, len(x))
		for i, el := range x {
			c, err := deepCopy(el, budget)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	return v, nil
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, 0, 4096); return &b }}

func getBuf() []byte { return (*bufPool.Get().(*[]byte))[:0] }

// putBuf keeps buffers up to 1 MiB so one huge document does not pin memory.
func putBuf(b []byte) {
	if cap(b) <= 1<<20 {
		bufPool.Put(&b)
	}
}

var poolPool = sync.Pool{New: func() any { return make(map[string]uint64, 256) }}

func getPool() map[string]uint64 { return poolPool.Get().(map[string]uint64) }

// putPool clears the map (keeping its buckets) unless it grew large.
func putPool(m map[string]uint64) {
	if len(m) <= 1<<14 {
		clear(m)
		poolPool.Put(m)
	}
}
