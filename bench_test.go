package jaro

import (
	"fmt"
	"testing"
)

var (
	benchWords  = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
	benchCities = []string{"London", "Paris", "New York", "Berlin", "Tokyo", "Warsaw"}
)

type xorshift uint64

func (r *xorshift) intn(n int) int {
	*r ^= *r << 13
	*r ^= *r >> 7
	*r ^= *r << 17
	return int(uint64(*r)>>33&0x7fffffff) % n
}

func benchUsers(n int) any {
	r := xorshift(0x9E3779B97F4A7C16)
	rows := make([]any, n)
	for i := range rows {
		rows[i] = map[string]any{
			"id": int64(i + 1), "name": benchWords[r.intn(8)] + " " + benchWords[r.intn(8)],
			"email": fmt.Sprintf("%s%d@example.com", benchWords[r.intn(8)], i), "age": int64(18 + r.intn(60)),
			"active": r.intn(2) == 0, "score": float64(r.intn(10000)) / 100, "city": benchCities[r.intn(6)],
		}
	}
	return map[string]any{"users": rows}
}

func benchEvents(n int) any {
	r := xorshift(0x1234567887654321)
	kinds := []string{"click", "view", "purchase", "scroll"}
	rows := make([]any, n)
	for i := range rows {
		rows[i] = map[string]any{
			"ts": int64(1700000000 + i*7), "kind": kinds[r.intn(4)], "user": int64(r.intn(500)),
			"attrs": map[string]any{"page": "/p/" + benchWords[r.intn(8)], "ref": benchCities[r.intn(6)]},
			"ms":    float64(r.intn(90000)) / 10,
		}
	}
	return map[string]any{"events": rows}
}

func BenchmarkCodec(b *testing.B) {
	sets := map[string]any{"users1000": benchUsers(1000), "events2000": benchEvents(2000)}
	for name, v := range sets {
		txt, _ := Marshal(v)
		bin, _ := MarshalBinary(v)
		b.Run("text_enc_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = Marshal(v)
			}
		})
		b.Run("text_dec_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = Unmarshal(txt)
			}
		})
		b.Run("bin_enc_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = MarshalBinary(v)
			}
		})
		b.Run("bin_dec_"+name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = UnmarshalBinary(bin)
			}
		})
	}
}
