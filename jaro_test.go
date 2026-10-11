package jaro

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var samples = map[string]any{
	"scalars": map[string]any{"a": int64(1), "b": 2.5, "c": true, "d": false, "e": nil, "f": "hi", "g": int64(-9007199254740991)},
	"quoting": map[string]any{"empty": "", "num": "123", "neg": "-1", "dot": ".5", "t": "true", "null": "~", "sp": "a b", "col": "a:b", "q": "say \"x\"", "nl": "a\nb", "bs": "a\\b", "uni": "héllo ✓", "ctl": "\x01"},
	"nested":  map[string]any{"o": map[string]any{"x": []any{int64(1), []any{}, map[string]any{}}}},
	"table": map[string]any{"rows": []any{
		map[string]any{"id": int64(1), "name": "ann", "tags": []any{"a", "b"}, "n": nil},
		map[string]any{"id": int64(2), "name": "bob smith", "tags": []any{}, "n": 1.5},
		map[string]any{"id": int64(300), "name": "ann", "tags": []any{"a"}, "n": "x"},
	}},
	"sparse": map[string]any{"rows": []any{
		map[string]any{"id": int64(1), "name": "ann", "email": "a@x.io"},
		map[string]any{"id": int64(2), "name": "bob"},
		map[string]any{"id": int64(3), "email": "c@x.io", "x": "^"},
		map[string]any{"id": int64(4), "name": "^", "^": "k"},
	}},
	"keyed": map[string]any{"versions": map[string]any{
		"1.0.0": map[string]any{"name": "x", "main": "a.js", "n": int64(1)},
		"1.1.0": map[string]any{"name": "x", "n": int64(2)},
		"2.0.0": map[string]any{"name": "^", "main": "b.js", "n": nil},
	}, "other": map[string]any{"a": int64(1), "b": map[string]any{"c": int64(2)}}},
	"nestedcols": map[string]any{"rows": []any{
		map[string]any{"id": int64(1), "user": map[string]any{"name": "ann", "id": int64(7), "geo": map[string]any{"lat": 1.5, "lon": 2.5}}, "t": "x"},
		map[string]any{"id": int64(2), "user": map[string]any{"name": "bob", "geo": map[string]any{"lat": 3.5, "lon": 4.5}}, "t": "y"},
		map[string]any{"id": int64(3), "user": map[string]any{"name": "^", "id": int64(8), "geo": map[string]any{}}, "t": "z"},
	}},
	"nestednull": []any{
		map[string]any{"a": int64(1), "u": map[string]any{"x": int64(1), "y": int64(2)}},
		map[string]any{"a": int64(2), "u": nil},
		map[string]any{"a": int64(3), "u": map[string]any{"x": int64(3), "y": int64(4)}},
	},
	"sparseempty": []any{map[string]any{"a": int64(1), "b": int64(2)}, map[string]any{}, map[string]any{"a": int64(3)}},
	"mixedarr":    []any{map[string]any{"a": int64(1)}, map[string]any{"b": int64(2)}, "s"},
	"toplevel":    "just a string",
	"topnum":      3.25,
	"emptyobj":    map[string]any{},
	"keyquote":    map[string]any{"a b": int64(1), "": int64(2), "1": int64(3)},
}

func TestRoundTrip(t *testing.T) {
	for name, v := range samples {
		want := canon(t, v)
		txt, err := Marshal(v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		got, err := Unmarshal(txt)
		if err != nil {
			t.Fatalf("%s: unmarshal %q: %v", name, txt, err)
		}
		if g := canon(t, got); g != want {
			t.Errorf("%s text: got %s want %s (jaro %q)", name, g, want, txt)
		}
		bin, err := MarshalBinary(v)
		if err != nil {
			t.Fatalf("%s: marshalbin: %v", name, err)
		}
		gotb, err := UnmarshalBinary(bin)
		if err != nil {
			t.Fatalf("%s: unmarshalbin: %v", name, err)
		}
		if g := canon(t, gotb); g != want {
			t.Errorf("%s bin: got %s want %s", name, g, want)
		}
		if !reflect.DeepEqual(got, gotb) {
			t.Errorf("%s: text and binary decode differ", name)
		}
	}
}

func TestTextShape(t *testing.T) {
	v := map[string]any{"rows": []any{
		map[string]any{"id": int64(1), "name": "ann"},
		map[string]any{"id": int64(2), "name": "bob"},
	}}
	b, _ := Marshal(v)
	if want := `rows:[|id name| 1 ann 2 bob]`; string(b) != want {
		t.Fatalf("got %q want %q", b, want)
	}
}

func TestRejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "{", "[1 2", `"abc`, "[|a b| 1 2 3]", "a:", "}", "a:1 b", `"\q"`, "[|| 1]", "[|a| 1] x"} {
		if _, err := Unmarshal([]byte(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
	for _, in := range [][]byte{nil, {0x0F, 0x01, 0x07, 0xff, 0xff, 0xff, 0xff, 0x0f}, {0x0F, 0x01, 0x06, 0x05}, {0x0F, 0x01, 0x04, 1, 2}, {0x0F, 0x01, 0x09, 0x00}, {0x0F, 0x01, 0x03}, {0x0F, 0x01, 0x02, 0x02}, {0x0F, 0x01, 0x08, 0x01, 0x03, 0x00}, {0x0F, 0x02, 0x00}, {0x00}} {
		if _, err := UnmarshalBinary(in); err == nil {
			t.Errorf("accepted binary % x", in)
		}
	}
}

func TestUnsupported(t *testing.T) {
	if _, err := Marshal(make(chan int)); err == nil {
		t.Error("chan accepted")
	}
	if _, err := MarshalBinary(make(chan int)); err == nil {
		t.Error("chan accepted (bin)")
	}
}

func FuzzText(f *testing.F) {
	f.Add([]byte(`a:1 b:[|x y| 1 2 3 4]`))
	f.Add([]byte(`"q\n" ~ true`))
	f.Fuzz(func(t *testing.T, in []byte) {
		v, err := Unmarshal(in)
		if err != nil {
			return
		}
		out, err := Marshal(v)
		if err != nil {
			return
		}
		v2, err := Unmarshal(out)
		if err != nil {
			t.Fatalf("re-decode of %q failed: %v", out, err)
		}
		if canonF(v) != canonF(v2) {
			t.Fatalf("unstable: %q", out)
		}
	})
}

func FuzzBinary(f *testing.F) {
	b, _ := MarshalBinary(samples["table"])
	f.Add(b)
	f.Fuzz(func(t *testing.T, in []byte) {
		v, err := UnmarshalBinary(in)
		if err != nil {
			return
		}
		out, err := MarshalBinary(v)
		if err != nil {
			return
		}
		if _, err := UnmarshalBinary(out); err != nil {
			t.Fatalf("re-decode failed: %v", err)
		}
	})
}

func canonF(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestAnchorsTextAndSpread(t *testing.T) {
	in := `# defaults
base:&b {host:h port:80}
a:*b
c:{*b port:81}
l:[*b *b]`
	v, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"host":"h","port":80},"base":{"host":"h","port":80},"c":{"host":"h","port":81},"l":[{"host":"h","port":80},{"host":"h","port":80}]}`
	if g := canon(t, v); g != want {
		t.Fatalf("got %s", g)
	}
	// copies are independent
	m := v.(map[string]any)
	m["a"].(map[string]any)["port"] = int64(1)
	if m["base"].(map[string]any)["port"] != int64(80) {
		t.Fatal("anchor aliased")
	}
}

func TestAnchorErrors(t *testing.T) {
	for _, in := range []string{`a:*x`, `a:&x 1 b:&x 2`, `a:{*x}`, `a:&x 1 b:{*x}`, `a:&`} {
		if _, err := Unmarshal([]byte(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestAnchorBomb(t *testing.T) {
	in := `a:&a0 [x x x x x x x x x x]`
	prev := "a0"
	for i := 1; i < 12; i++ {
		n := "a" + string(rune('0'+i%10)) + string(rune('a'+i))
		in += " b" + n + ":&" + n + " [" + strings.Repeat("*"+prev+" ", 10) + "]"
		prev = n
	}
	if _, err := Unmarshal([]byte(in)); err == nil {
		t.Fatal("expansion bomb accepted")
	}
}

func TestDedupRoundTrip(t *testing.T) {
	cfg := map[string]any{"host": "db.internal.example", "port": int64(5432), "opts": []any{"ssl", "retry"}}
	v := map[string]any{"a": cfg, "b": map[string]any{"x": cfg}, "c": []any{cfg, cfg}}
	plain, _ := Marshal(v)
	dd, err := MarshalOpts(v, Options{Dedup: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(dd) >= len(plain) {
		t.Fatalf("dedup %d not smaller than %d: %s", len(dd), len(plain), dd)
	}
	got, err := Unmarshal(dd)
	if err != nil {
		t.Fatalf("%v: %s", err, dd)
	}
	if canon(t, got) != canon(t, v) {
		t.Fatalf("text dedup mismatch: %s", dd)
	}
	bp, _ := MarshalBinary(v)
	bd, err := MarshalBinaryOpts(v, Options{Dedup: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(bd) >= len(bp) {
		t.Fatalf("bin dedup %d not smaller than %d", len(bd), len(bp))
	}
	gb, err := UnmarshalBinary(bd)
	if err != nil || canon(t, gb) != canon(t, v) {
		t.Fatalf("bin dedup mismatch: %v", err)
	}
}

func TestDedupTableRoundTrip(t *testing.T) {
	tags := []any{"alpha-tag", "beta-tag", "gamma-tag"}
	rows := []any{}
	for i := 0; i < 5; i++ {
		rows = append(rows, map[string]any{"id": int64(i), "tags": tags})
	}
	v := map[string]any{"rows": rows, "again": tags}
	for _, o := range []Options{{Dedup: true}, {Dedup: true, MinDedup: 4}} {
		tb, _ := MarshalOpts(v, o)
		got, err := Unmarshal(tb)
		if err != nil || canon(t, got) != canon(t, v) {
			t.Fatalf("text %v: %s", err, tb)
		}
		bb, _ := MarshalBinaryOpts(v, o)
		gb, err := UnmarshalBinary(bb)
		if err != nil || canon(t, gb) != canon(t, v) {
			t.Fatalf("bin %v", err)
		}
	}
}

func TestComments(t *testing.T) {
	v, err := Unmarshal([]byte("a:1 # one\n# whole line\nb:\"#not\" c:[1 #x\n 2]"))
	if err != nil {
		t.Fatal(err)
	}
	if g := canon(t, v); g != `{"a":1,"b":"#not","c":[1,2]}` {
		t.Fatal(g)
	}
	b, _ := Marshal(map[string]any{"h": "#tag", "s": "*x", "a": "&y"})
	if v2, err := Unmarshal(b); err != nil || canon(t, v2) != `{"a":"\u0026y","h":"#tag","s":"*x"}` {
		t.Fatalf("%v %s %s", err, b, canon(t, v2))
	}
}

func mustSchema(t *testing.T, src string) *Schema {
	t.Helper()
	def, err := Unmarshal([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Compile(def)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSchema(t *testing.T) {
	s := mustSchema(t, `
id:int(1..)
name:"str(1..10)"
email:"str(/^[^@ ]+@[^@ ]+$/)"
role:enum(admin,user)
age?:int(0..150)
nick:str?
tags:[str 0..2]
meta:{...:any}`)
	ok := map[string]any{"id": int64(3), "name": "ann", "email": "a@b.c", "role": "user", "nick": nil,
		"tags": []any{"x"}, "meta": map[string]any{"z": int64(1)}}
	if errs := s.Validate(ok); len(errs) != 0 {
		t.Fatalf("valid rejected: %v", errs)
	}
	bad := map[string]any{"id": int64(0), "name": "", "email": "nope", "role": "root", "age": 200.5, "nick": int64(1),
		"tags": []any{"a", "b", int64(3)}, "meta": int64(2), "extra": true}
	errs := s.Validate(bad)
	want := map[string]bool{
		"$.id": true, "$.name": true, "$.email": true, "$.role": true, "$.age": true,
		"$.nick": true, "$.tags": true, "$.tags[2]": true, "$.meta": true, "$": true,
	}
	got := map[string]bool{}
	for _, e := range errs {
		got[e.Path] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("missing error at %s (got %v)", p, errs)
		}
	}
	if errs := s.Validate(map[string]any{}); len(errs) == 0 {
		t.Error("empty object passed")
	}
}

func TestSchemaEither(t *testing.T) {
	s := mustSchema(t, `v:"either(int,str(1..3))"`)
	for v, wantOK := range map[any]bool{int64(1): true, "ab": true, "abcd": false, true: false, 1.5: false} {
		if (len(s.Validate(map[string]any{"v": v})) == 0) != wantOK {
			t.Errorf("%v: want ok=%v", v, wantOK)
		}
	}
}

func TestSchemaCompileErrors(t *testing.T) {
	for _, def := range []any{"nope", "int(a..b)", "str(/[/)", "enum()", []any{}, []any{"int", "x"}, "int(1)"} {
		if _, err := Compile(def); err == nil {
			t.Errorf("accepted %v", def)
		}
	}
}

func TestSchemaReuseWithAnchors(t *testing.T) {
	s := mustSchema(t, `
home:&addr {street:str city:str}
work:*addr`)
	good := map[string]any{"home": map[string]any{"street": "s", "city": "c"}, "work": map[string]any{"street": "s", "city": "c"}}
	if errs := s.Validate(good); len(errs) != 0 {
		t.Fatal(errs)
	}
	if errs := s.Validate(map[string]any{"home": good["home"], "work": map[string]any{"street": "s"}}); len(errs) != 1 {
		t.Fatalf("%v", errs)
	}
}

func TestFromJSON(t *testing.T) {
	v, err := FromJSON([]byte(`{"big":9007199254740993,"f":1.5,"l":[1,2]}`))
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["big"] != int64(9007199254740993) {
		t.Fatal("lost precision")
	}
}

func TestColumnsRoundTrip(t *testing.T) {
	mk := func(vals ...any) map[string]any {
		rows := make([]any, len(vals))
		for i, v := range vals {
			rows[i] = map[string]any{"v": v, "i": int64(i * 3)}
		}
		return map[string]any{"rows": rows}
	}
	big := int64(1) << 60
	cases := map[string]any{
		"sorted ints":  mk(int64(1000), int64(1007), int64(1019), int64(1020)),
		"random ints":  mk(int64(900), int64(-4), int64(77777), int64(3)),
		"huge ints":    mk(big, -big, big+1, int64(0)),
		"decimals":     mk(12.34, 0.5, int64(7), 99.999999),
		"too precise":  mk(0.1234567, 1.0/3, 2.5, 1e-9),
		"big decimals": mk(1e15+0.5, 2.5, int64(3), 4.25),
		"bools":        mk(true, false, true, true, false, false, true, false, true),
		"dict":         mk("aa", "bb", "aa", "bb", "aa", "bb"),
		"unique str":   mk("a1", "b2", "c3", "d4"),
		"mixed":        mk("a", int64(1), nil, true),
		"nested objs":  mk(map[string]any{"p": "x", "q": 1.5}, map[string]any{"p": "y", "q": int64(2)}, map[string]any{"p": "x", "q": 3.25}),
		"ragged objs":  mk(map[string]any{"p": "x"}, map[string]any{"q": "y"}),
		"plain arrays": map[string]any{"a": []any{int64(1), int64(2), int64(3), int64(4), int64(5)}, "b": []any{1.5, 2.5, 3.5, 4.5}, "c": []any{"x", "y", "x", "y"}, "d": []any{true, false, true, false}},
		"nulls in col": mk(nil, nil, nil, nil),
	}
	for name, v := range cases {
		b, err := MarshalBinary(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := UnmarshalBinary(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if canon(t, got) != canon(t, v) {
			t.Errorf("%s: got %s want %s", name, canon(t, got), canon(t, v))
		}
		txt, _ := Marshal(v)
		tv, err := Unmarshal(txt)
		if err != nil || canon(t, tv) != canon(t, got) {
			t.Errorf("%s: text and binary disagree", name)
		}
	}
}

func TestColumnsShrink(t *testing.T) {
	rows := make([]any, 200)
	for i := range rows {
		rows[i] = map[string]any{"ts": int64(1700000000 + i*7), "ok": i%3 == 0, "kind": []string{"a", "b", "c"}[i%3], "ms": float64(i%90) / 10}
	}
	v := map[string]any{"rows": rows}
	b, _ := MarshalBinary(v)
	js, _ := ToJSON(v)
	if len(b)*6 > len(js) {
		t.Fatalf("binary %d not <1/6 of json %d", len(b), len(js))
	}
}

func TestBinaryColumnHostile(t *testing.T) {
	for _, in := range [][]byte{
		{0x0F, 0x01, 0x0C, 0x05, 0x01},                        // delta column, no data
		{0x0F, 0x01, 0x0C, 0x05, 0x02, 0x09},                  // bad decimal scale
		{0x0F, 0x01, 0x0C, 0x05, 0x03, 0x01, 0x41, 'x', 0x09}, // bad dict index
		{0x0F, 0x01, 0x0C, 0x05, 0x04},                        // bool bitset truncated
		{0x0F, 0x01, 0x0C, 0x05, 0x09},                        // bad kind
		{0x0F, 0x01, 0x0C, 0x02, 0x05, 0x00},                  // empty obj column
	} {
		if _, err := UnmarshalBinary(in); err == nil {
			t.Errorf("accepted % x", in)
		}
	}
}

// Golden vectors freeze wire format v1. Changing any of these breaks stored
// data and needs a new binVersion.
func TestGoldenVectors(t *testing.T) {
	v := map[string]any{
		"name": "platform", "ok": true, "none": nil, "n": int64(-3), "f": 2.5,
		"rows": []any{
			map[string]any{"id": int64(1), "tag": "a b"},
			map[string]any{"id": int64(2), "tag": "c"},
		},
	}
	txt, _ := Marshal(v)
	if want := `f:2.5 n:-3 name:platform none:~ ok:true rows:[|id tag| 1 "a b" 2 c]`; string(txt) != want {
		t.Fatalf("text vector changed:\n got %s\nwant %s", txt, want)
	}
	bin, _ := MarshalBinary(v)
	if bin[0] != 0x0F || bin[1] != 0x01 || bin[2] != tObj || bin[3] != 6 {
		t.Fatalf("binary header changed: % x", bin[:4])
	}
	back, err := UnmarshalBinary(bin)
	if err != nil || canon(t, back) != canon(t, v) {
		t.Fatalf("binary vector round trip: %v", err)
	}
	doc := []byte{0x0F, 0x01, 0x08, 0x02, 0x41, 'k', 0x83, 0x42, 'k', 'l', 0x03, 0x7B}
	got, err := UnmarshalBinary(doc)
	if err != nil {
		t.Fatal(err)
	}
	if canon(t, got) != `{"k":3,"kl":-62}` {
		t.Fatalf("hand-written v1 document decodes to %s", canon(t, got))
	}
}

func TestInvalidUTF8(t *testing.T) {
	if _, err := Unmarshal([]byte("\xe7:0 \xef:0")); err == nil {
		t.Error("text decoder accepted invalid UTF-8")
	}
	if _, err := UnmarshalBinary([]byte{0x0F, 0x01, 0x41, 0xff}); err == nil {
		t.Error("binary decoder accepted invalid UTF-8")
	}
	v := map[string]any{"k\xff": "v\xfe"}
	tb, _ := Marshal(v)
	got, err := Unmarshal(tb)
	if err != nil || canon(t, got) != `{"k�":"v�"}` {
		t.Fatalf("text: %v %s", err, tb)
	}
	bb, _ := MarshalBinary(v)
	if got, err = UnmarshalBinary(bb); err != nil || canon(t, got) != `{"k�":"v�"}` {
		t.Fatalf("binary: %v", err)
	}
}

func TestInternDoesNotChangeTypes(t *testing.T) {
	v, err := Unmarshal([]byte(`a:"1.5" b:1.5 c:"1e3" d:1e3 e:"-2" f:-2 g:"~" h:~ i:"true" j:true k:"x" l:x`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":"1.5","b":1.5,"c":"1e3","d":1000,"e":"-2","f":-2,"g":"~","h":null,"i":"true","j":true,"k":"x","l":"x"}`
	if g := canon(t, v); g != want {
		t.Fatalf("got %s\nwant %s", g, want)
	}
}

func TestFastDecimalMatchesParseFloat(t *testing.T) {
	for _, tok := range []string{"1.5", "0.1", "-0.25", "123456.789", "0.000001", "99.99", "3.0", "-0.0", "123456789012.345", "1234567890123.45", "9007199254740993.5", "0.30000000000000004", "1.", ".5", "-.5", "00.5", "1.2.3"} {
		want, werr := strconv.ParseFloat(tok, 64)
		v, err := Unmarshal([]byte("x:" + tok))
		got, isFloat := v.(map[string]any)["x"].(float64)
		if err != nil {
			t.Fatalf("%s: %v", tok, err)
		}
		if werr != nil { // not a float: must stay a string
			if _, isStr := v.(map[string]any)["x"].(string); !isStr {
				t.Errorf("%s: want string, got %T", tok, v.(map[string]any)["x"])
			}
			continue
		}
		if want == 0 { // zero (including -0) normalises to the integer 0
			if v.(map[string]any)["x"] != int64(0) {
				t.Errorf("%s: want int 0, got %v (%T)", tok, v.(map[string]any)["x"], v.(map[string]any)["x"])
			}
			continue
		}
		if !isFloat || math.Float64bits(got) != math.Float64bits(want) {
			t.Errorf("%s: got %v (%T) want %v", tok, v.(map[string]any)["x"], v.(map[string]any)["x"], want)
		}
	}
}

func TestSparseTable(t *testing.T) {
	rows := []any{
		map[string]any{"id": int64(1), "name": "ann", "email": "a@x.io"},
		map[string]any{"id": int64(2), "name": "bob"},
		map[string]any{"id": int64(3), "email": "c@x.io"},
	}
	txt, err := Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[|email id name| a@x.io 1 ann ^ 2 bob c@x.io 3 ^]"; string(txt) != want {
		t.Errorf("got %q want %q", txt, want)
	}
	got, err := Unmarshal(txt)
	if err != nil {
		t.Fatal(err)
	}
	if g, w := canon(t, got), canon(t, rows); g != w {
		t.Errorf("got %s want %s", g, w)
	}
	if _, ok := got.([]any)[1].(map[string]any)["email"]; ok {
		t.Error("absent cell decoded as a present key")
	}
	sparse := []any{map[string]any{"a": int64(1)}, map[string]any{"b": int64(2)}, map[string]any{"c": int64(3)}}
	if txt, _ = Marshal(sparse); strings.Contains(string(txt), "|") {
		t.Errorf("mostly absent rows must stay objects: %s", txt)
	}
}

func TestKeyedTable(t *testing.T) {
	v := map[string]any{"v": map[string]any{
		"a b": map[string]any{"x": int64(1), "y": "p"},
		"c":   map[string]any{"x": int64(2)},
	}}
	txt, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if want := `v:{|x y| "a b" 1 p c 2 ^}`; string(txt) != want {
		t.Errorf("got %q want %q", txt, want)
	}
	got, err := Unmarshal(txt)
	if err != nil {
		t.Fatal(err)
	}
	if g, w := canon(t, got), canon(t, v); g != w {
		t.Errorf("got %s want %s", g, w)
	}
	for _, bad := range []string{`{|x| a}`, `{|x| a 1`, `{|| a}`, `{|x| 1 2}`} {
		if _, err := Unmarshal([]byte(bad)); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestBinarySparseHostile(t *testing.T) {
	rows := []any{
		map[string]any{"a": int64(1), "b": "x"},
		map[string]any{"a": int64(2)},
		map[string]any{"a": int64(3), "b": "y"},
		map[string]any{"a": int64(4), "b": "z"},
	}
	bin, err := MarshalBinary(rows)
	if err != nil {
		t.Fatal(err)
	}
	if bin[2] != tSparse {
		t.Fatalf("want a sparse table, got tag 0x%02x", bin[2])
	}
	got, err := UnmarshalBinary(bin)
	if err != nil {
		t.Fatal(err)
	}
	if g, w := canon(t, got), canon(t, rows); g != w {
		t.Errorf("got %s want %s", g, w)
	}
	for i := 3; i < len(bin); i++ {
		if _, err := UnmarshalBinary(bin[:i]); err == nil {
			t.Errorf("truncation at %d decoded without error", i)
		}
	}
	for i := range bin {
		bad := append([]byte(nil), bin...)
		bad[i] ^= 0xFF
		_, _ = UnmarshalBinary(bad)
	}
}

func TestNestedColumns(t *testing.T) {
	rows := []any{
		map[string]any{"id": int64(1), "user": map[string]any{"n": "ann", "k": int64(7)}},
		map[string]any{"id": int64(2), "user": map[string]any{"n": "bob"}},
	}
	txt, err := Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[|id user{k n}| 1 7 ann 2 ^ bob]"; string(txt) != want {
		t.Errorf("got %q want %q", txt, want)
	}
	got, err := Unmarshal(txt)
	if err != nil {
		t.Fatal(err)
	}
	if g, w := canon(t, got), canon(t, rows); g != w {
		t.Errorf("got %s want %s", g, w)
	}
	for _, bad := range []string{"[|a b{}| 1]", "[|a b{c d}| 1 2]", "[|a b{c d}| 1 2 3 4]", "[|a{b{c{d{e{f{g{h{i{j}}}}}}}}}| 1]"} {
		if _, err := Unmarshal([]byte(bad)); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestDecimalColumnKeepsMixedMagnitudes(t *testing.T) {
	in := []any{705137.6, 7512.906316, 1.328554778e+08, 4.300036195495e+11}
	b, err := MarshalBinary(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := UnmarshalBinary(b)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range in {
		if got := out.([]any)[i]; got != want {
			t.Errorf("value %d: got %v want %v", i, got, want)
		}
	}
}
