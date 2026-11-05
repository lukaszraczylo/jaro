package jaro

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

// textPlan is the reference planner: it keys every container by its
// standalone text, which costs a serialisation per container and ancestor.
type textPlan struct {
	counts map[string]int
	names  map[string]uint64
	min    int
	col    bool
}

func newTextPlan(v any, min int, col bool) *textPlan {
	p := &textPlan{counts: map[string]int{}, names: map[string]uint64{}, min: min, col: col}
	p.visit(v)
	return p
}

func (p *textPlan) key(v any) (string, bool) {
	switch v.(type) {
	case map[string]any, []any:
	default:
		return "", false
	}
	b, err := Marshal(v)
	if err != nil || len(b) < p.min {
		return "", false
	}
	return string(b), true
}

func (p *textPlan) visit(v any) {
	if k, ok := p.key(v); ok {
		p.counts[k]++
		if p.counts[k] > 1 {
			return
		}
	}
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			p.visit(x[k])
		}
	case []any:
		cols, ok := tableKeys(x)
		switch {
		case !ok:
			for _, el := range x {
				p.visit(el)
			}
		case p.col:
			for _, c := range cols {
				for _, row := range x {
					p.visit(row.(map[string]any)[c])
				}
			}
		default:
			for _, row := range x {
				for _, c := range cols {
					p.visit(row.(map[string]any)[c])
				}
			}
		}
	}
}

func (p *textPlan) ref(v any) (idx uint64, use, def bool) {
	k, ok := p.key(v)
	if !ok || p.counts[k] < 2 {
		return 0, false, false
	}
	if i, seen := p.names[k]; seen {
		return i, true, false
	}
	i := uint64(len(p.names))
	p.names[k] = i
	return i, false, true
}

func encodeWith(v any, pl planner, binary bool) ([]byte, error) {
	if binary {
		e := &binEnc{buf: []byte{binMagic, binVersion}, pool: map[string]uint64{}, dd: pl}
		if err := e.value(v, 0); err != nil {
			return nil, err
		}
		return e.buf, nil
	}
	e := &textEnc{dd: pl}
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		return e.buf, e.objBody(m, 1)
	}
	err := e.value(v, 0)
	return e.buf, err
}

var randWords = []string{"a", "bb", "hello world", "a\xffb", "a\xff\xffb", "x\xfe", "x\xff\xfe\xfd\xfc", "ü", "~", "true", "12", ""}

func randTree(r *rand.Rand, depth int, pool *[]any) any {
	if len(*pool) > 0 && r.Intn(6) == 0 { // repeated and shared sub-trees
		return (*pool)[r.Intn(len(*pool))]
	}
	k := r.Intn(12)
	if depth <= 0 && k > 5 {
		k = r.Intn(6)
	}
	var v any
	switch k {
	case 0:
		v = nil
	case 1:
		v = r.Intn(2) == 0
	case 2:
		v = int64(r.Intn(40) - 5)
	case 3:
		v = []float64{1, 1.5, 2.0, 0.1, 3, 1e20}[r.Intn(6)]
	case 4:
		v = randWords[r.Intn(len(randWords))]
	case 5:
		v = []any{3, int8(3), uint16(3), float32(3), float64(3)}[r.Intn(5)]
	case 6, 7, 8:
		a := make([]any, r.Intn(5))
		for i := range a {
			a[i] = randTree(r, depth-1, pool)
		}
		v = a
	case 9, 10:
		m := map[string]any{}
		for i, n := 0, r.Intn(5); i < n; i++ {
			m[[]string{"id", "name", "k\xff", "k\xfe\xff", "tags", "x", "y", "z"}[r.Intn(8)]] = randTree(r, depth-1, pool)
		}
		v = m
	default: // rows that form a table
		a := make([]any, 2+r.Intn(4))
		for i := range a {
			a[i] = map[string]any{"id": int64(i), "name": randWords[r.Intn(3)], "v": []any{int64(1), "p"}}
		}
		v = a
	}
	if r.Intn(4) == 0 {
		*pool = append(*pool, v)
	}
	return v
}

// The class-based planner must choose the same anchors as the text-keyed one.
func TestDedupPlanMatchesTextKeys(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		var pool []any
		v := randTree(rand.New(rand.NewSource(seed)), 6, &pool) //nolint:gosec // seeded fixtures must repeat
		for _, min := range []int{1, 2, 4, 8, 24, 100} {
			for _, bin := range []bool{false, true} {
				t.Run(fmt.Sprintf("seed%d/min%d/bin%v", seed, min, bin), func(t *testing.T) {
					want, werr := encodeWith(v, newTextPlan(v, min, bin), bin)
					got, gerr := encodeWith(v, newDedup(v, min, bin), bin)
					if (werr == nil) != (gerr == nil) || !bytes.Equal(got, want) {
						t.Fatalf("plans differ\n got %q (%v)\nwant %q (%v)", got, gerr, want, werr)
					}
				})
			}
		}
	}
}

func TestDedupLongChainIsLinear(t *testing.T) {
	var v any = "leaf"
	for range maxDepth - 8 {
		v = []any{v, "pad pad pad pad pad pad pad pad"}
	}
	if _, err := MarshalOpts(v, Options{Dedup: true}); err != nil {
		t.Fatal(err)
	}
}

func TestDedupPlannerPoolIsRaceFree(t *testing.T) {
	var pool []any
	v := randTree(rand.New(rand.NewSource(7)), 6, &pool) //nolint:gosec // seeded fixtures must repeat
	want, err := MarshalOpts(v, Options{Dedup: true, MinDedup: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			for range 50 {
				got, err := MarshalOpts(v, Options{Dedup: true, MinDedup: 4})
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("pooled planner changed the output: %v", err)
				}
			}
		})
	}
}
