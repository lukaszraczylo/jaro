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
	defaultMinDedup = 24
	maxExpand       = 1 << 24 // nodes a decoder may materialise through references
)

// Options tunes encoding.
type Options struct {
	// Dedup replaces repeated sub-trees with anchors and back-references.
	Dedup bool
	// MinDedup is the smallest sub-tree (in text bytes) worth anchoring.
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
	ids    map[nodeID]uint32 // container -> class
	byHash map[uint64]uint32 // signature hash -> newest class+1 with that hash
	strs   map[string]uint32 // sanitised string -> id
	cls    []class
	sigs   []elem // signature arena
	stk    []elem // scratch: child elements of the containers being walked
	ks     []string
	vks    []string // scratch for visit
	counts []int32  // per class: times counted by visit
	names  []int32  // per class: anchor index+1
	enc    textEnc
	nnames uint64
	poison uint64
	min    int
	col    bool
}

// planner decides, per container at emission time, where anchors go.
type planner interface {
	ref(v any) (idx uint64, use, def bool)
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
	return &dedup{ids: map[nodeID]uint32{}, byHash: map[uint64]uint32{}, strs: map[string]uint32{}}
}}

// maxPooledClasses keeps one huge document from pinning memory in the pool.
const maxPooledClasses = 1 << 18

// newDedup plans the anchors of v. Call release when the encoder is done.
func newDedup(v any, min int, columnMajor bool) *dedup {
	d := dedupPool.Get().(*dedup)
	d.min, d.col = min, columnMajor
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

func (d *dedup) visit(v any) {
	if c, ok := d.counted(v); ok {
		d.counts[c]++
		if d.counts[c] > 1 {
			return
		}
	}
	switch x := v.(type) {
	case map[string]any:
		base := len(d.vks)
		d.vks = appendSortedKeys(d.vks, x)
		for _, k := range d.vks[base:] {
			d.visit(x[k])
		}
		d.vks = d.vks[:base]
	case []any:
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
