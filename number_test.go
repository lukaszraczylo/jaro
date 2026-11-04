package jaro

import (
	"fmt"
	"testing"
)

// SPEC.md: integral values below 2^53 are integers, whatever their spelling.
func TestIntegralLiteralsDecodeAsInt(t *testing.T) {
	cases := []struct {
		tok   string
		isInt bool
		want  float64
	}{
		{"1", true, 1},
		{"1.0", true, 1},
		{"-1.0", true, -1},
		{"100.00", true, 100},
		{"0.0", true, 0},
		{"-0.0", true, 0},
		{"1e9", true, 1e9},
		{"1E3", true, 1000},
		{"2.5e1", true, 25},
		{"1.5", false, 1.5},
		{"0.25", false, 0.25},
		{"1e-2", false, 0.01},
		{"1e300", false, 1e300},
		{"9007199254740992.0", false, 9007199254740992},
		{"123456789012345.5", false, 123456789012345.5},
	}
	for _, c := range cases {
		t.Run(c.tok, func(t *testing.T) {
			v, err := Unmarshal([]byte("[" + c.tok + " 0]"))
			if err != nil {
				t.Fatal(err)
			}
			got := v.([]any)[0]
			switch x := got.(type) {
			case int64:
				if !c.isInt || float64(x) != c.want {
					t.Fatalf("Unmarshal: got int64 %d", x)
				}
			case float64:
				if c.isInt || x != c.want {
					t.Fatalf("Unmarshal: got float64 %v, want int", x)
				}
			default:
				t.Fatalf("Unmarshal: got %T", got)
			}
			doc, err := Parse([]byte("[" + c.tok + " 0]"))
			if err != nil {
				t.Fatal(err)
			}
			n := doc.Root().Index(0)
			if (n.Kind() == KindInt) != c.isInt || n.Float() != c.want {
				t.Fatalf("Parse: kind %v float %v, want isInt=%v", n.Kind(), n.Float(), c.isInt)
			}
		})
	}
}

// Pins lean4/Jaro/Number.lean canonDec_eq_canonQ: a plain decimal is an int
// iff 10^k divides its mantissa.
func TestDecimalIntegerRuleMatchesLean(t *testing.T) {
	for k := 1; k <= 6; k++ {
		for v := int64(0); v < 3000; v++ {
			for _, neg := range []string{"", "-"} {
				tok := fmt.Sprintf("%s%0*d", neg, k+1, v)
				tok = tok[:len(tok)-k] + "." + tok[len(tok)-k:]
				got, err := Unmarshal([]byte("[" + tok + " 0]"))
				if err != nil {
					t.Fatal(err)
				}
				_, isInt := got.([]any)[0].(int64)
				if want := v%pow10Int[k] == 0; isInt != want {
					t.Fatalf("%s: isInt=%v want %v", tok, isInt, want)
				}
			}
		}
	}
}
