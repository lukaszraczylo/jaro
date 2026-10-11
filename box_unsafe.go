//go:build jaro_unsafe

package jaro

import "unsafe"

// slabChunk is the number of values per slab chunk. A chunk stays alive
// while any value boxed in it is referenced.
const slabChunk = 256

// eface mirrors the runtime layout of an empty interface.
type eface struct{ typ, data unsafe.Pointer }

var (
	floatType = typeWord(float64(0))
	intType   = typeWord(int64(0))
	strType   = typeWord("")
)

func typeWord(v any) unsafe.Pointer { return (*eface)(unsafe.Pointer(&v)).typ }

// box builds an interface value of type t that points at p, without
// allocating. The caller must never write *p afterwards.
func box(t, p unsafe.Pointer) (v any) {
	e := (*eface)(unsafe.Pointer(&v))
	e.typ, e.data = t, p
	return v
}

// slab boxes decoded numbers and strings in chunked slices, so each value
// costs no allocation of its own.
type slab struct {
	floats []float64
	ints   []int64
	strs   []string
}

func (s *slab) float(f float64) any {
	if len(s.floats) == cap(s.floats) {
		s.floats = make([]float64, 0, slabChunk)
	}
	s.floats = append(s.floats, f)
	return box(floatType, unsafe.Pointer(&s.floats[len(s.floats)-1]))
}

func (s *slab) int(i int64) any {
	if i >= 0 && i < boxedInts {
		return boxInt(i)
	}
	if len(s.ints) == cap(s.ints) {
		s.ints = make([]int64, 0, slabChunk)
	}
	s.ints = append(s.ints, i)
	return box(intType, unsafe.Pointer(&s.ints[len(s.ints)-1]))
}

func (s *slab) str(v string) any {
	if len(s.strs) == cap(s.strs) {
		s.strs = make([]string, 0, slabChunk)
	}
	s.strs = append(s.strs, v)
	return box(strType, unsafe.Pointer(&s.strs[len(s.strs)-1]))
}
