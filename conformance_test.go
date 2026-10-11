package jaro

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

const (
	conformanceFile  = "conformance/vectors.json"
	conformanceWrite = "JARO_WRITE_CONFORMANCE"
)

type textVector struct {
	Name string          `json:"name"`
	Jaro string          `json:"jaro"`
	JSON json.RawMessage `json:"json"`
}

type binVector struct {
	Name string          `json:"name"`
	Hex  string          `json:"hex"`
	JSON json.RawMessage `json:"json"`
}

type treeVector struct {
	Name string          `json:"name"`
	JSON json.RawMessage `json:"json"`
}

// vectors is the language-neutral conformance suite. Decoders must reproduce
// JSON for Text and Binary and reject Bad*. Encoders must round-trip Trees
// through their own decoder.
type vectors struct {
	Text      []textVector `json:"text"`
	Binary    []binVector  `json:"binary"`
	Trees     []treeVector `json:"trees"`
	BadText   []string     `json:"badText"`
	BadTextHx []string     `json:"badTextHex"`
	BadBinary []string     `json:"badBinaryHex"`
	Version   int          `json:"version"`
}

// handText are decode-only cases written by hand: syntax that Marshal never emits.
var handText = []struct{ name, in, want string }{
	{"anchor-spread", "# defaults\nbase:&b {host:h port:80}\na:*b\nc:{*b port:81}\nl:[*b *b]", `{"a":{"host":"h","port":80},"base":{"host":"h","port":80},"c":{"host":"h","port":81},"l":[{"host":"h","port":80},{"host":"h","port":80}]}`},
	{"comments", "a:1 # one\n# whole line\nb:\"#not\" c:[1 #x\n 2]", `{"a":1,"b":"#not","c":[1,2]}`},
	{"braces-optional", "a:1 b:2", `{"a":1,"b":2}`},
	{"braces-explicit", "{a:1 b:2}", `{"a":1,"b":2}`},
	{"scalar-top", "~", `null`},
	{"bool-top", "true", `true`},
	{"number-forms", "[1 -2 3.5 .5 -.5 1e3 0 -0 007]", `[1,-2,3.5,0.5,-0.5,1000,0,0,7]`},
	{"quoted-escapes", `["a\"b" "a\\b" "a\nb" "a\tb" "a\rb" "é" "A"]`, `["a\"b","a\\b","a\nb","a\tb","a\rb","é","A"]`},
	{"table-hand", "[|id name| 1 ann 2 bob]", `[{"id":1,"name":"ann"},{"id":2,"name":"bob"}]`},
	{"table-sparse", "[|id name| 1 ann 2 ^ ^ cid]", `[{"id":1,"name":"ann"},{"id":2},{"name":"cid"}]`},
	{"table-nested-columns", "[|id u{n g{a b}}| 1 ann 1 2 2 bob 3 4]", `[{"id":1,"u":{"n":"ann","g":{"a":1,"b":2}}},{"id":2,"u":{"n":"bob","g":{"a":3,"b":4}}}]`},
	{"keyed-table", "{|a b| x 1 2 y 3 ^}", `{"x":{"a":1,"b":2},"y":{"a":3}}`},
	{"empty-containers", "[[] {} [|a| ]]", `[[],{},[]]`},
	{"quoted-key", `{"a b":1 "":2 "1":3}`, `{"":2,"1":3,"a b":1}`},
	{"unicode-bare", "héllo:✓", `{"héllo":"✓"}`},
	{"caret-string", `a:"^"`, `{"a":"^"}`},
	{"surrogate-pair", `"\ud83d\ude00"`, `"\ud83d\ude00"`},
	{"number-grammar", "[1_000 0x10 1. +5 1e 1e5 .5e1 Inf NaN -.5 1E+2]", `["1_000","0x10",1,"+5","1e",100000,5,"Inf","NaN",-0.5,100]`},
	{"spread-overrides-member", "base:&b {a:1 b:2} x:{a:9 *b}", `{"base":{"a":1,"b":2},"x":{"a":1,"b":2}}`},
	{"member-overrides-spread", "base:&b {a:1 b:2} x:{*b a:9}", `{"base":{"a":1,"b":2},"x":{"a":9,"b":2}}`},
	{"bare-ampersand-quoted", `a:"&x" b:"*y"`, `{"a":"&x","b":"*y"}`},
}

var badText = []string{"", "{", "[1 2", `"abc`, "[|a b| 1 2 3]", "a:", "}", "a:1 b", `"\q"`, "[|| 1]", "[|a| 1] x", "a:*x", "a:&x 1 b:&x 2", "a:{*x}", "a:&x 1 b:{*x}", "a:&", "a:1 a:2", "{a:1 a:2}", "[|a a| 1 2]", "{|a b| x 1 2 x 3 4}", `"\u+041"`, `"\ud83d"`, `"\ud83dx"`, `"\ude00"`, `"\ud83d\u0041"`, "a:{b:1 b:2}", "b:&b {x:1} a:{*b y:1 y:2}", "[|a b{c c}| 1 2 3]"}

var badBinary = [][]byte{nil, {0x0F, 0x01, 0x07, 0xff, 0xff, 0xff, 0xff, 0x0f}, {0x0F, 0x01, 0x06, 0x05}, {0x0F, 0x01, 0x04, 1, 2}, {0x0F, 0x01, 0x09, 0x00}, {0x0F, 0x01, 0x03}, {0x0F, 0x01, 0x02, 0x02}, {0x0F, 0x01, 0x08, 0x01, 0x03, 0x00}, {0x0F, 0x02, 0x00}, {0x00},
	{0x0F, 0x01, 0x04, 0, 0, 0, 0, 0, 0, 0xf8, 0x7f}, {0x0F, 0x01, 0x04, 0, 0, 0, 0, 0, 0, 0xf0, 0x7f},
	{0x0F, 0x01, 0x0D, 0x01, 0x41, 0x61, 0x03, 0x01, 0x83, 0x00, 0x81, 0x82},
	{0x0F, 0x01, 0x08, 0x02, 0x41, 0x61, 0x80, 0x41, 0x61, 0x81}}

// randomTable builds rows that exercise delta, decimal, dict, bool and object columns.
func randomTable(r *rand.Rand, rows int, sparse bool) []any {
	words := []string{"red", "green", "blue", "amber", "violet"}
	out := make([]any, rows)
	for i := range rows {
		row := map[string]any{
			"id":    int64(1000 + i*3),
			"price": float64(r.Intn(100000)) / 100,
			"tag":   words[r.Intn(len(words))],
			"on":    r.Intn(2) == 0,
			"loc":   map[string]any{"x": int64(r.Intn(50)), "y": float64(r.Intn(900)) / 10},
			"note":  words[r.Intn(len(words))] + " item",
		}
		if sparse && r.Intn(3) == 0 {
			delete(row, "note")
		}
		out[i] = row
	}
	return out
}

func buildVectors(t *testing.T) vectors {
	t.Helper()
	v := vectors{Version: 1}
	names := make([]string, 0, len(samples))
	all := map[string]any{}
	for n, s := range samples {
		names = append(names, n)
		all[n] = s
	}
	r := rand.New(rand.NewSource(42)) //nolint:gosec // fixed seed keeps the vectors reproducible
	for _, n := range []string{"dense", "sparse"} {
		names = append(names, "random-"+n)
		all["random-"+n] = map[string]any{"rows": randomTable(r, 60, n == "sparse")}
	}
	names = append(names, "decimal-mixed")
	all["decimal-mixed"] = []any{705137.6, 7512.906316, 1.328554778e+08, 4.300036195495e+11}
	names = append(names, "bigint")
	all["bigint"] = map[string]any{"max": int64(9223372036854775807), "min": int64(-9223372036854775808), "big": int64(1) << 60}
	sort.Strings(names)
	for _, n := range names {
		js := mustJSON(t, all[n])
		txt, err := Marshal(all[n])
		if err != nil {
			t.Fatal(err)
		}
		v.Text = append(v.Text, textVector{n, string(txt), js})
		v.Trees = append(v.Trees, treeVector{n, js})
		for _, o := range []struct {
			suffix string
			opts   Options
		}{{"", Options{}}, {"-dedup", Options{Dedup: true}}} {
			b, err := MarshalBinaryOpts(all[n], o.opts)
			if err != nil {
				t.Fatal(err)
			}
			v.Binary = append(v.Binary, binVector{n + o.suffix, hex.EncodeToString(b), js})
		}
	}
	for _, h := range handText {
		v.Text = append(v.Text, textVector{h.name, h.in, json.RawMessage(h.want)})
	}
	v.BadText = badText
	v.BadTextHx = []string{"fffe", "613aff"} // invalid UTF-8
	for _, b := range badBinary {
		v.BadBinary = append(v.BadBinary, hex.EncodeToString(b))
	}
	return v
}

func mustJSON(t *testing.T, x any) json.RawMessage {
	t.Helper()
	b, err := ToJSON(x)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameJSON compares two JSON documents as trees, with integers exact.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	norm := func(in []byte) string {
		d := json.NewDecoder(bytes.NewReader(in))
		d.UseNumber()
		var x any
		if err := d.Decode(&x); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	return norm(a) == norm(b)
}

func TestConformanceVectors(t *testing.T) {
	want := buildVectors(t)
	if os.Getenv(conformanceWrite) != "" {
		b, err := json.MarshalIndent(want, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(conformanceFile), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(conformanceFile, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(conformanceFile)
	if err != nil {
		t.Fatalf("%v (run with %s=1)", err, conformanceWrite)
	}
	var got vectors
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Text {
		t.Run("text/"+c.Name, func(t *testing.T) {
			v, err := Unmarshal([]byte(c.Jaro))
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, mustJSON(t, v), c.JSON) {
				t.Fatalf("got %s want %s", mustJSON(t, v), c.JSON)
			}
		})
	}
	for _, c := range got.Binary {
		t.Run("binary/"+c.Name, func(t *testing.T) {
			b, err := hex.DecodeString(c.Hex)
			if err != nil {
				t.Fatal(err)
			}
			v, err := UnmarshalBinary(b)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, mustJSON(t, v), c.JSON) {
				t.Fatalf("got %s want %s", mustJSON(t, v), c.JSON)
			}
		})
	}
	for _, in := range got.BadText {
		if _, err := Unmarshal([]byte(in)); err == nil {
			t.Errorf("accepted text %q", in)
		}
	}
	for _, h := range got.BadTextHx {
		b, _ := hex.DecodeString(h)
		if _, err := Unmarshal(b); err == nil {
			t.Errorf("accepted text bytes %s", h)
		}
	}
	for _, h := range got.BadBinary {
		b, _ := hex.DecodeString(h)
		if _, err := UnmarshalBinary(b); err == nil {
			t.Errorf("accepted binary %s", h)
		}
	}
}
