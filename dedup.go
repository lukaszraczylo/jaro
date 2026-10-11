package jaro

import (
	"errors"
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
	counts map[string]int
	names  map[string]uint64
	min    int
	col    bool
}

func newDedup(v any, min int, columnMajor bool) *dedup {
	d := &dedup{min: min, col: columnMajor, counts: map[string]int{}, names: map[string]uint64{}}
	d.visit(v)
	return d
}

func (d *dedup) key(v any) (string, bool) {
	switch v.(type) {
	case map[string]any, []any:
	default:
		return "", false
	}
	b, err := Marshal(v)
	if err != nil || len(b) < d.min {
		return "", false
	}
	return string(b), true
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
