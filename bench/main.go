// Command bench writes each dataset in each format to -out and prints size
// and speed. Token counts come from bench/tokens.py over the same files.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
	gojson "github.com/goccy/go-json"
	"github.com/klauspost/compress/zstd"
	"github.com/lukaszraczylo/jaro"
	ugorji "github.com/ugorji/go/codec"
	"github.com/vmihailenco/msgpack/v5"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"
)

var words = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo", "lima"}
var cities = []string{"London", "Paris", "New York", "Berlin", "Tokyo", "Warsaw"}

func users(n int) any {
	r := newRNG(1)
	rows := make([]any, n)
	for i := range rows {
		rows[i] = map[string]any{
			"id":     int64(i + 1),
			"name":   words[r.Intn(len(words))] + " " + words[r.Intn(len(words))],
			"email":  fmt.Sprintf("%s%d@example.com", words[r.Intn(len(words))], i),
			"age":    int64(18 + r.Intn(60)),
			"active": r.Intn(2) == 0,
			"score":  float64(r.Intn(10000)) / 100,
			"city":   cities[r.Intn(len(cities))],
		}
	}
	return map[string]any{"users": rows}
}

func config() any {
	svc := func(name string, port int64) any {
		return map[string]any{
			"name": name, "port": port, "replicas": int64(3),
			"resources": map[string]any{"cpu": "500m", "memory": "512Mi", "limits": map[string]any{"cpu": "1", "memory": "1Gi"}},
			"env":       []any{"LOG_LEVEL", "info", "REGION", "eu-west-1"},
			"probe":     map[string]any{"path": "/healthz", "interval": int64(10), "timeout": int64(2)},
		}
	}
	var s []any
	for i := 0; i < 40; i++ {
		s = append(s, svc(fmt.Sprintf("svc-%d", i), int64(8000+i)))
	}
	return map[string]any{"version": int64(2), "name": "platform", "services": s,
		"owners": []any{"team-a", "team-b"}, "flags": map[string]any{"beta": true, "legacy": false, "audit": nil}}
}

func events(n int) any {
	r := newRNG(2)
	kinds := []string{"click", "view", "purchase", "scroll"}
	rows := make([]any, n)
	for i := range rows {
		rows[i] = map[string]any{
			"ts":    int64(1700000000 + i*7),
			"kind":  kinds[r.Intn(4)],
			"user":  int64(r.Intn(500)),
			"attrs": map[string]any{"page": "/p/" + words[r.Intn(len(words))], "ref": cities[r.Intn(len(cities))]},
			"ms":    float64(r.Intn(90000)) / 10,
		}
	}
	return map[string]any{"events": rows}
}

func mk(name string, enc func(any) ([]byte, error), dec func([]byte) (any, error)) codec {
	return codec{name: name, enc: enc, dec: dec}
}

var (
	cborDec, _ = cbor.DecOptions{DefaultMapType: reflect.TypeOf(map[string]any{})}.DecMode()
	mpHandle   = newMsgpackHandle()
)

func newMsgpackHandle() *ugorji.MsgpackHandle {
	h := &ugorji.MsgpackHandle{}
	h.MapType = reflect.TypeOf(map[string]any{})
	h.RawToString = true
	return h
}

func bsonDec(b []byte) (any, error) {
	var v map[string]any
	dec := bson.NewDecoder(bson.NewDocumentReader(bytes.NewReader(b)))
	dec.DefaultDocumentM()
	return v, dec.Decode(&v)
}

func pbEnc(v any) ([]byte, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("protobuf needs an object")
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(s)
}

func pbDec(b []byte) (any, error) {
	s := &structpb.Struct{}
	if err := proto.Unmarshal(b, s); err != nil {
		return nil, err
	}
	return s.AsMap(), nil
}

type codec struct {
	enc  func(any) ([]byte, error)
	dec  func([]byte) (any, error)
	name string
}

func main() {
	out := flag.String("out", "out", "output dir")
	data := flag.String("data", "", "directory of .json files to benchmark instead of the generated sets")
	only := flag.String("formats", "", "comma-separated format names to run (default all)")
	flag.Parse()
	must(os.MkdirAll(*out, 0o750))
	codecs := []codec{
		mk("json", func(v any) ([]byte, error) { return json.Marshal(v) }, func(b []byte) (any, error) { var v any; return v, json.Unmarshal(b, &v) }),
		mk("yaml", func(v any) ([]byte, error) { return yaml.Marshal(v) }, func(b []byte) (any, error) { var v any; return v, yaml.Unmarshal(b, &v) }),
		mk("jaro", jaro.Marshal, jaro.Unmarshal),
		mk("jaro+dedup", func(v any) ([]byte, error) { return jaro.MarshalOpts(v, jaro.Options{Dedup: true}) }, jaro.Unmarshal),
		mk("jarob", jaro.MarshalBinary, jaro.UnmarshalBinary),
		mk("jarob+dedup", func(v any) ([]byte, error) { return jaro.MarshalBinaryOpts(v, jaro.Options{Dedup: true}) }, jaro.UnmarshalBinary),
		mk("go-json", func(v any) ([]byte, error) { return gojson.Marshal(v) }, func(b []byte) (any, error) { var v any; return v, gojson.Unmarshal(b, &v) }),
		mk("cbor", cbor.Marshal, func(b []byte) (any, error) { var v any; return v, cborDec.Unmarshal(b, &v) }),
		mk("msgpack", func(v any) ([]byte, error) { return msgpack.Marshal(v) }, func(b []byte) (any, error) { var v any; return v, msgpack.Unmarshal(b, &v) }),
		mk("ugorji-mp", func(v any) ([]byte, error) {
			var out []byte
			return out, ugorji.NewEncoderBytes(&out, mpHandle).Encode(v)
		}, func(b []byte) (any, error) { var v any; return v, ugorji.NewDecoderBytes(b, mpHandle).Decode(&v) }),
		mk("bson", func(v any) ([]byte, error) { return bson.Marshal(v) }, bsonDec),
		mk("protobuf", pbEnc, pbDec),
	}
	if *only != "" {
		keep := map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			keep[n] = true
		}
		codecs = slices.DeleteFunc(codecs, func(c codec) bool { return !keep[c.name] })
	}
	zenc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	must(err)
	sets := []struct {
		v    any
		name string
	}{{name: "users1000", v: users(1000)}, {name: "config", v: config()}, {name: "events2000", v: events(2000)}}
	if *data != "" {
		sets = loadSets(*data)
	}
	fmt.Printf("%-11s %-12s %9s %9s %9s %11s %11s\n", "dataset", "format", "bytes", "gzip", "zstd", "enc µs", "dec µs")
	fmt.Printf("%-11s %-12s %11s %9s %11s %9s %10s\n", "", "", "enc B/op", "allocs", "dec B/op", "allocs", "live B")
	for _, ds := range sets {
		want, _ := json.Marshal(ds.v)
		for _, c := range codecs {
			b, err := c.enc(ds.v)
			if err != nil {
				panic(err)
			}
			got, err := c.dec(b)
			if err != nil {
				panic(fmt.Sprint(c.name, ": ", err))
			}
			if g, _ := json.Marshal(got); !bytes.Equal(g, want) && c.name != "yaml" {
				fmt.Fprintf(os.Stderr, "WARN %s round trip differs on %s: %.160s | want %.160s\n", c.name, ds.name, g, want)
			}
			must(os.WriteFile(filepath.Join(*out, ds.name+"."+c.name), b, 0o600))
			enc := testing.Benchmark(func(tb *testing.B) {
				tb.ReportAllocs()
				for i := 0; i < tb.N; i++ {
					_, _ = c.enc(ds.v)
				}
			})
			dec := testing.Benchmark(func(tb *testing.B) {
				tb.ReportAllocs()
				for i := 0; i < tb.N; i++ {
					_, _ = c.dec(b)
				}
			})
			var gz bytes.Buffer
			w, err := gzip.NewWriterLevel(&gz, 6)
			must(err)
			_, err = w.Write(b)
			must(err)
			must(w.Close())
			fmt.Printf("%-11s %-12s %9d %9d %9d %11.1f %11.1f\n", ds.name, c.name, len(b), gz.Len(), len(zenc.EncodeAll(b, nil)),
				float64(enc.NsPerOp())/1000, float64(dec.NsPerOp())/1000)
			fmt.Printf("%-11s %-12s %11d %9d %11d %9d %10d\n", "", "", enc.AllocedBytesPerOp(), enc.AllocsPerOp(),
				dec.AllocedBytesPerOp(), dec.AllocsPerOp(), liveBytes(c.dec, b))
		}
	}
}

func loadSets(dir string) []struct {
	v    any
	name string
} {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	must(err)
	var sets []struct {
		v    any
		name string
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // operator-supplied benchmark corpus
		must(err)
		var v any
		must(json.Unmarshal(raw, &v))
		sets = append(sets, struct {
			v    any
			name string
		}{v: v, name: strings.TrimSuffix(filepath.Base(p), ".json")})
	}
	return sets
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// rng is a fixed-seed xorshift generator so datasets are reproducible.
type rng struct{ s uint64 }

func newRNG(seed uint64) *rng { return &rng{s: seed*0x9E3779B97F4A7C15 + 1} }

func (r *rng) Intn(n int) int {
	r.s ^= r.s << 13
	r.s ^= r.s >> 7
	r.s ^= r.s << 17
	return int(r.s>>33&math.MaxInt32) % n
}

// liveBytes is the heap a decoded document retains after a full GC.
func liveBytes(dec func([]byte) (any, error), b []byte) uint64 {
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	v, err := dec(b)
	must(err)
	runtime.GC()
	runtime.ReadMemStats(&m1)
	runtime.KeepAlive(v)
	if m1.HeapAlloc < m0.HeapAlloc {
		return 0
	}
	return m1.HeapAlloc - m0.HeapAlloc
}
