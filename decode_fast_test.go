package jaro

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// parsedValue decodes "x:"+tok with Unmarshal and Parse and requires the same tree.
func parsedValue(t *testing.T, tok string) any {
	t.Helper()
	src := []byte("x:" + tok)
	v, err := Unmarshal(src)
	if err != nil {
		t.Fatalf("%q: Unmarshal: %v", tok, err)
	}
	doc, err := Parse(src)
	if err != nil {
		t.Fatalf("%q: Parse: %v", tok, err)
	}
	if got := toAny(doc.Root()); !sameTree(got, v) {
		t.Fatalf("%q: Parse %s differs from Unmarshal %s", tok, canon(t, got), canon(t, v))
	}
	return v.(map[string]any)["x"]
}

func TestQuotedEscapeRuns(t *testing.T) {
	long := strings.Repeat("abcdefghij", 40)
	cases := []struct{ raw, want string }{
		{`"plain"`, "plain"},
		{`""`, ""},
		{`"a\nb"`, "a\nb"},
		{`"\"\""`, `""`},
		{`"tail\\"`, `tail\`},
		{`"\\\\"`, `\\`},
		{`"lead\tmid\rend"`, "lead\tmid\rend"},
		{`"café 😀 ok"`, "café \U0001F600 ok"},
		{`"` + long + `\n` + long + `\"` + long + `"`, long + "\n" + long + `"` + long},
		{`"` + long + `"`, long},
		{`"é\n✓ "`, "é\n✓ "},
	}
	for _, c := range cases {
		if got := parsedValue(t, c.raw); got != c.want {
			t.Errorf("%s: got %q want %q", c.raw, got, c.want)
		}
	}
	// Two strings in one document: the second must not disturb the first.
	v, err := Unmarshal([]byte(`a:"x\ny" b:"p\tq" c:"plain"`))
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["a"] != "x\ny" || m["b"] != "p\tq" || m["c"] != "plain" {
		t.Errorf("got %v", m)
	}
}

func TestQuotedEscapeErrors(t *testing.T) {
	for _, raw := range []string{`"abc`, `"abc\`, `"a\"`, `"a\qb"`, `"\u12"`, `"\u12zz"`, `"\ud800"`, `"x\ud800A"`, `"ok\n`} {
		src := []byte("x:" + raw)
		if _, err := Unmarshal(src); err == nil {
			t.Errorf("Unmarshal(%q): want error", src)
		}
		if _, err := Parse(src); err == nil {
			t.Errorf("Parse(%q): want error", src)
		}
	}
}

func TestPlainNumbersMatchStrconv(t *testing.T) {
	for _, tok := range []string{
		"0", "-0", "7", "-7", "1234567890123456", "123456789012345678", "1234567890123456789", "-123456789012345678",
		"1.5", "-65.613616999999977", "43.420273000000009", "0.1", "0.30000000000000004", "123456789012345.5",
		"1234567890123456.5", "-0.0", "0.0", "5.", ".5", "1e3", "1.5e-3", "9007199254740993", "00.5", "1.2.3",
	} {
		got := parsedValue(t, tok)
		if i, err := strconv.ParseInt(tok, 10, 64); err == nil {
			if got != i {
				t.Errorf("%s: got %v (%T) want int %d", tok, got, got, i)
			}
			continue
		}
		want, err := strconv.ParseFloat(tok, 64)
		if err != nil || want == 0 { // not a number, or zero (which decodes as the integer 0)
			continue
		}
		if g, ok := got.(float64); !ok || math.Float64bits(g) != math.Float64bits(want) {
			t.Errorf("%s: got %v (%T) want %v", tok, got, got, want)
		}
	}
}

func TestParseWideObjectKeys(t *testing.T) {
	obj := func(keys ...string) string {
		var sb strings.Builder
		sb.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(" ")
			}
			sb.WriteString(k + ":1")
		}
		sb.WriteString("}")
		return sb.String()
	}
	ok := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	// Several wide objects share key ids: a key seen in an earlier object is not a duplicate.
	src := "[" + obj(ok...) + " " + obj(ok[1:]...) + " " + obj(ok...) + "]"
	if _, err := Parse([]byte(src)); err != nil {
		t.Fatalf("wide objects: %v", err)
	}
	dup := append(append([]string{}, ok...), "c")
	if _, err := Parse([]byte("[" + obj(ok...) + " " + obj(dup...) + "]")); err == nil {
		t.Fatal("duplicate key in a wide object: want error")
	}
	if _, err := Unmarshal([]byte(obj(dup...))); err == nil {
		t.Fatal("Unmarshal duplicate key in a wide object: want error")
	}
}

func TestBinaryKeyForms(t *testing.T) {
	long := strings.Repeat("k", 40)
	v := map[string]any{
		"short":  int64(1),
		long:     "v",
		"list":   []any{map[string]any{long: int64(1), "short": "x"}, map[string]any{long: int64(2), "short": "y"}},
		"nested": map[string]any{long: map[string]any{"short": long}},
	}
	bin, err := MarshalBinary(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalBinary(bin)
	if err != nil {
		t.Fatal(err)
	}
	if !sameTree(got, v) {
		t.Errorf("got %s want %s", canon(t, got), canon(t, v))
	}
	for cut := 2; cut < len(bin); cut++ { // truncated keys must fail cleanly
		if _, err := UnmarshalBinary(bin[:cut]); err == nil {
			t.Fatalf("truncated at %d: want error", cut)
		}
	}
}
