package jaro

import (
	"bytes"
	"errors"
	"hash/maphash"
	"reflect"
	"sync"
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
type dedup struct {
	counts  map[sig]int
	names   map[sig]uint64
	keys    map[nodeID]dedupKey
	first   map[sig]any // first container seen per signature, to verify matches
	scratch []byte
	check   []byte
	min     int
	col     bool
}

// sig is a 128-bit hash of a container's text plus its length. Equal
// signatures are verified against the stored first container, so a hash
// collision can only lose a dedup, never merge unequal values.
type sig struct {
	a, b uint64
	n    int
}

var sigSeedA, sigSeedB = maphash.MakeSeed(), maphash.MakeSeed()

// nodeID identifies a container by address and length. The tree is not
// mutated while the plan is built, so the key of a node never changes.
type nodeID struct {
	ptr uintptr
	n   int
}

type dedupKey struct {
	s  sig
	ok bool
}

func newDedup(v any, min int, columnMajor bool) *dedup {
	d := &dedup{
		min: min, col: columnMajor,
		counts: map[sig]int{}, names: map[sig]uint64{},
		keys: map[nodeID]dedupKey{}, first: map[sig]any{},
	}
	d.visit(v)
	return d
}

// key returns the signature of container v when its text is at least d.min
// bytes. It serialises each container once; visit and ref share the result.
func (d *dedup) key(v any) (sig, bool) {
	var id nodeID
	switch x := v.(type) {
	case map[string]any:
		id = nodeID{reflect.ValueOf(x).Pointer(), len(x)}
	case []any:
		if len(x) == 0 {
			return d.build(v)
		}
		id = nodeID{reflect.ValueOf(x).Pointer(), len(x)}
	default:
		return sig{}, false
	}
	if k, hit := d.keys[id]; hit {
		return k.s, k.ok
	}
	s, ok := d.build(v)
	d.keys[id] = dedupKey{s, ok}
	return s, ok
}

func (d *dedup) text(buf []byte, v any) ([]byte, bool) {
	e := textEnc{buf: buf[:0]}
	var err error
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		err = e.objBody(m, 1)
	} else {
		err = e.value(v, 0)
	}
	return e.buf, err == nil
}

func (d *dedup) build(v any) (sig, bool) {
	var ok bool
	d.scratch, ok = d.text(d.scratch, v)
	if !ok || len(d.scratch) < d.min {
		return sig{}, false
	}
	s := sig{maphash.Bytes(sigSeedA, d.scratch), maphash.Bytes(sigSeedB, d.scratch), len(d.scratch)}
	rep, seen := d.first[s]
	if !seen {
		d.first[s] = v
		return s, true
	}
	d.check, ok = d.text(d.check, rep)
	if !ok || !bytes.Equal(d.check, d.scratch) {
		return sig{}, false
	}
	return s, true
}

func (d *dedup) visit(v any) {
	if k, ok := d.key(v); ok {
		d.counts[k]++
		if d.counts[k] > 1 {
			return
		}
	}
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			d.visit(x[k])
		}
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
	k, ok := d.key(v)
	if !ok || d.counts[k] < 2 {
		return 0, false, false
	}
	if i, seen := d.names[k]; seen {
		return i, true, false
	}
	i := uint64(len(d.names))
	d.names[k] = i
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
