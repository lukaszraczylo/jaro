//go:build !jaro_unsafe

package jaro

// slab boxes decoded numbers and strings. The default build uses the
// runtime's own boxing, which allocates once per value; build with
// -tags jaro_unsafe to batch these allocations (see box_unsafe.go).
type slab struct{}

func (*slab) float(f float64) any { return f }
func (*slab) int(i int64) any     { return boxInt(i) }
func (*slab) str(s string) any    { return s }
