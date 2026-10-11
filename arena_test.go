package jaro

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// toAny rebuilds the Unmarshal tree from a Node.
func toAny(n Node) any {
	switch n.Kind() {
	case KindNull:
		return nil
	case KindBool:
		return n.Bool()
	case KindInt:
		return n.Int()
	case KindFloat:
		return n.Float()
	case KindString:
		return n.String()
	case KindArray:
		out := make([]any, n.Len())
		for i := range out {
			out[i] = toAny(n.Index(i))
		}
		return out
	}
	out := make(map[string]any, n.Len())
	for i := 0; i < n.Len(); i++ {
		m := n.Index(i)
		out[m.Key()] = toAny(m)
	}
	return out
}

func sameTree(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && (x == y || math.IsNaN(x) && math.IsNaN(y))
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameTree(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !sameTree(v, w) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

func TestParseMatchesUnmarshal(t *testing.T) {
	for name, v := range samples {
		txt, err := Marshal(v)
		if err != nil {
			t.Fatal(name, err)
		}
		want, err := Unmarshal(txt)
		if err != nil {
			t.Fatal(name, err)
		}
		doc, err := Parse(txt)
		if err != nil {
			t.Fatalf("%s: parse %q: %v", name, txt, err)
		}
		if got := toAny(doc.Root()); !sameTree(got, want) {
			t.Errorf("%s: got %s want %s", name, canon(t, got), canon(t, want))
		}
	}
}

func TestParseRealFiles(t *testing.T) {
	dir := os.Getenv(corpusEnv)
	if dir == "" {
		t.Skip(corpusEnv + " not set")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // operator-supplied corpus
		if err != nil {
			t.Fatal(err)
		}
		v, err := FromJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		txt, err := Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		want, err := Unmarshal(txt)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := Parse(txt)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(p), err)
		}
		if !sameTree(toAny(doc.Root()), want) {
			t.Errorf("%s: Parse and Unmarshal differ", filepath.Base(p))
		}
	}
}

func TestParseAccessors(t *testing.T) {
	doc, err := Parse([]byte(`a:1 b:[x "y z" 2.5 ~ true] c:{d:e}`))
	if err != nil {
		t.Fatal(err)
	}
	root := doc.Root()
	b, _ := root.Get("b")
	if b.Len() != 5 || b.Index(1).String() != "y z" || b.Index(2).Float() != 2.5 || !b.Index(4).Bool() || b.Index(3).Kind() != KindNull {
		t.Errorf("unexpected array contents")
	}
	c, _ := root.Get("c")
	if d, ok := c.Get("d"); !ok || d.String() != "e" {
		t.Errorf("unexpected object member")
	}
	if _, ok := root.Get("missing"); ok {
		t.Error("found a missing key")
	}
}

func FuzzParse(f *testing.F) {
	for _, v := range samples {
		if txt, err := Marshal(v); err == nil {
			f.Add(txt)
		}
	}
	f.Add([]byte(`a:"x\ny" b:[|k m{n o}| 1 2 3 ^ 4 5]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, perr := Parse(data)
		want, uerr := Unmarshal(data)
		if (perr == nil) != (uerr == nil) {
			// Parse rejects anchors and spreads that Unmarshal accepts.
			return
		}
		if perr == nil && !sameTree(toAny(doc.Root()), want) {
			t.Fatalf("Parse and Unmarshal differ on %q", data)
		}
	})
}

func TestParseScalarHasNoChildren(t *testing.T) {
	d, err := Parse([]byte("{a:hello b:[1 2]}"))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := d.Root().Get("a")
	if !ok || s.Len() != 0 {
		t.Fatalf("string node: ok=%v len=%d", ok, s.Len())
	}
	if _, ok := s.Get("x"); ok {
		t.Fatal("Get on a string must report absent")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Index on a string must panic")
		}
	}()
	s.Index(0)
}

func TestReparse(t *testing.T) {
	in := []byte("[|id name| 1 a 2 b 3 c]")
	want, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	wantTree := toAny(want.Root())
	d := new(Doc)
	for range 50 {
		if err := d.Reparse(in); err != nil {
			t.Fatal(err)
		}
		if !sameTree(toAny(d.Root()), wantTree) {
			t.Fatal("tree differs after buffer reuse")
		}
		if err := d.Reparse([]byte("{a:1 b:[2 3] c:{d:e}}")); err != nil {
			t.Fatal(err)
		}
		if err := d.Reparse([]byte("{a:")); err == nil {
			t.Fatal("bad input must fail")
		}
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	for _, in := range []string{"a:1 a:2", "{a:{b:1 b:2}}", "[|a a| 1 2]", "{|a b| x 1 2 x 3 4}", `"\ud83d"`} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
	d, err := Parse([]byte("[1_000 0x10 1e]"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if k := d.Root().Index(i).Kind(); k != KindString {
			t.Errorf("token %d: kind %v, want string", i, k)
		}
	}
}
