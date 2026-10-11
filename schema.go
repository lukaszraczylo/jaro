package jaro

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxErrors = 64

// Error is one validation failure at a JSONPath-like location.
type Error struct{ Path, Msg string }

func (e Error) Error() string { return e.Path + ": " + e.Msg }

// Schema validates decoded JARO/JSON data. Build one with Compile.
//
// A schema is itself JARO data:
//
//	string  type expression: any str int num bool null, optionally with an
//	        argument and a trailing ? for nullable:
//	        str(1..64) str(/^[a-z]+$/) int(0..100) num(..1.5)
//	        enum(a,b,c) either(int,str) str?
//	object  field types; a key ending in ? is optional; the key ... types
//	        every extra field (default: extra fields are rejected)
//	array   [T] list of T; [T 1..5] with a length range
//	other   a literal that the value must equal
type Schema struct{ root checker }

type checker interface {
	check(v any, path string, errs *[]Error)
}

// Compile builds a Schema from schema data.
func Compile(def any) (*Schema, error) {
	c, err := compile(def)
	if err != nil {
		return nil, err
	}
	return &Schema{root: c}, nil
}

// Validate returns all violations (at most 64); nil means valid.
func (s *Schema) Validate(v any) []Error {
	var errs []Error
	s.root.check(v, "$", &errs)
	return errs
}

func fail(errs *[]Error, path, format string, a ...any) {
	if len(*errs) < maxErrors {
		*errs = append(*errs, Error{path, fmt.Sprintf(format, a...)})
	}
}

func compile(def any) (checker, error) {
	switch x := def.(type) {
	case string:
		return compileExpr(x)
	case map[string]any:
		return compileObject(x)
	case []any:
		return compileArray(x)
	default:
		return literal{def}, nil
	}
}

type rng struct {
	lo, hi       float64
	hasLo, hasHi bool
}

func parseRange(s string) (rng, error) {
	a, b, ok := strings.Cut(s, "..")
	if !ok {
		return rng{}, fmt.Errorf("bad range %q", s)
	}
	var r rng
	var err error
	if a != "" {
		if r.lo, err = strconv.ParseFloat(a, 64); err != nil {
			return rng{}, fmt.Errorf("bad range %q", s)
		}
		r.hasLo = true
	}
	if b != "" {
		if r.hi, err = strconv.ParseFloat(b, 64); err != nil {
			return rng{}, fmt.Errorf("bad range %q", s)
		}
		r.hasHi = true
	}
	return r, nil
}

func (r rng) has(f float64) bool {
	return (!r.hasLo || f >= r.lo) && (!r.hasHi || f <= r.hi)
}

func (r rng) String() string {
	var lo, hi string
	if r.hasLo {
		lo = strconv.FormatFloat(r.lo, 'g', -1, 64)
	}
	if r.hasHi {
		hi = strconv.FormatFloat(r.hi, 'g', -1, 64)
	}
	return lo + ".." + hi
}

type prim struct {
	re       *regexp.Regexp
	kind     string
	enum     []string
	union    []checker
	r        rng
	hasR     bool
	nullable bool
}

func compileExpr(s string) (checker, error) {
	p := &prim{}
	if strings.HasSuffix(s, "?") {
		p.nullable = true
		s = s[:len(s)-1]
	}
	name, arg, hasArg := strings.Cut(s, "(")
	if hasArg {
		if !strings.HasSuffix(arg, ")") {
			return nil, fmt.Errorf("bad type %q: missing )", s)
		}
		arg = arg[:len(arg)-1]
	}
	p.kind = name
	switch name {
	case "any", "bool", "null":
		if hasArg {
			return nil, fmt.Errorf("type %s takes no argument", name)
		}
	case "str", "int", "num":
		if !hasArg {
			break
		}
		if name == "str" && strings.HasPrefix(arg, "/") && strings.HasSuffix(arg, "/") && len(arg) >= 2 {
			re, err := regexp.Compile(arg[1 : len(arg)-1])
			if err != nil {
				return nil, fmt.Errorf("bad regex in %q: %w", s, err)
			}
			p.re = re
			break
		}
		r, err := parseRange(arg)
		if err != nil {
			return nil, fmt.Errorf("type %q: %w", s, err)
		}
		p.r, p.hasR = r, true
	case "enum":
		if !hasArg || arg == "" {
			return nil, fmt.Errorf("enum needs values")
		}
		p.enum = strings.Split(arg, ",")
	case "either":
		if !hasArg || arg == "" {
			return nil, fmt.Errorf("either needs types")
		}
		for _, part := range strings.Split(arg, ",") {
			c, err := compileExpr(part)
			if err != nil {
				return nil, err
			}
			p.union = append(p.union, c)
		}
	default:
		return nil, fmt.Errorf("unknown type %q", name)
	}
	return p, nil
}

func kindOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "str"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		if n, ok, err := normNum(x); ok && err == nil {
			if n.isInt {
				return "int"
			}
			return "num"
		}
		return fmt.Sprintf("%T", v)
	}
}

func scalarText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "~"
	case bool:
		return strconv.FormatBool(x)
	}
	if n, ok, _ := normNum(v); ok {
		if n.isInt {
			return strconv.FormatInt(n.i, 10)
		}
		return strconv.FormatFloat(n.f, 'g', -1, 64)
	}
	return ""
}

func (p *prim) check(v any, path string, errs *[]Error) {
	k := kindOf(v)
	if k == "null" && p.nullable {
		return
	}
	switch p.kind {
	case "any":
	case "null", "bool":
		if k != p.kind {
			fail(errs, path, "want %s, got %s", p.kind, k)
		}
	case "str":
		s, ok := v.(string)
		if !ok {
			fail(errs, path, "want str, got %s", k)
			return
		}
		if p.hasR && !p.r.has(float64(utf8.RuneCountInString(s))) {
			fail(errs, path, "length %d outside %s", utf8.RuneCountInString(s), p.r)
		}
		if p.re != nil && !p.re.MatchString(s) {
			fail(errs, path, "%q does not match /%s/", s, p.re)
		}
	case "int", "num":
		n, ok, _ := normNum(v)
		if !ok || (p.kind == "int" && !n.isInt) {
			fail(errs, path, "want %s, got %s", p.kind, k)
			return
		}
		f := n.f
		if n.isInt {
			f = float64(n.i)
		}
		if p.hasR && !p.r.has(f) {
			fail(errs, path, "%s outside %s", scalarText(v), p.r)
		}
	case "enum":
		t := scalarText(v)
		for _, e := range p.enum {
			if e == t && k != "object" && k != "array" {
				return
			}
		}
		fail(errs, path, "%s not in enum(%s)", t, strings.Join(p.enum, ","))
	case "either":
		for _, c := range p.union {
			var sub []Error
			c.check(v, path, &sub)
			if len(sub) == 0 {
				return
			}
		}
		fail(errs, path, "%s matches none of the allowed types", k)
	}
}

type field struct {
	c        checker
	name     string
	optional bool
}

type object struct {
	extra  checker
	fields []field
}

func compileObject(m map[string]any) (checker, error) {
	o := &object{}
	for _, k := range sortedKeys(m) {
		c, err := compile(m[k])
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		if k == "..." {
			o.extra = c
			continue
		}
		f := field{name: k, c: c}
		if strings.HasSuffix(k, "?") && len(k) > 1 {
			f.name, f.optional = k[:len(k)-1], true
		}
		o.fields = append(o.fields, f)
	}
	return o, nil
}

func (o *object) check(v any, path string, errs *[]Error) {
	m, ok := v.(map[string]any)
	if !ok {
		fail(errs, path, "want object, got %s", kindOf(v))
		return
	}
	known := make(map[string]struct{}, len(o.fields))
	for _, f := range o.fields {
		known[f.name] = struct{}{}
		x, present := m[f.name]
		if !present {
			if !f.optional {
				fail(errs, path, "missing field %q", f.name)
			}
			continue
		}
		f.c.check(x, path+"."+f.name, errs)
	}
	for _, k := range sortedKeys(m) {
		if _, ok := known[k]; ok {
			continue
		}
		if o.extra == nil {
			fail(errs, path, "unexpected field %q", k)
			continue
		}
		o.extra.check(m[k], path+"."+k, errs)
	}
}

type array struct {
	elem checker
	n    rng
	hasN bool
}

func compileArray(a []any) (checker, error) {
	if len(a) < 1 || len(a) > 2 {
		return nil, fmt.Errorf("array schema must be [T] or [T min..max]")
	}
	el, err := compile(a[0])
	if err != nil {
		return nil, err
	}
	out := &array{elem: el}
	if len(a) == 2 {
		s, ok := a[1].(string)
		if !ok {
			return nil, fmt.Errorf("array length must be a range like 1..5")
		}
		if out.n, err = parseRange(s); err != nil {
			return nil, err
		}
		out.hasN = true
	}
	return out, nil
}

func (a *array) check(v any, path string, errs *[]Error) {
	arr, ok := v.([]any)
	if !ok {
		fail(errs, path, "want array, got %s", kindOf(v))
		return
	}
	if a.hasN && !a.n.has(float64(len(arr))) {
		fail(errs, path, "length %d outside %s", len(arr), a.n)
	}
	for i, x := range arr {
		a.elem.check(x, path+"["+strconv.Itoa(i)+"]", errs)
	}
}

type literal struct{ want any }

func (l literal) check(v any, path string, errs *[]Error) {
	if kindOf(v) != kindOf(l.want) || scalarText(v) != scalarText(l.want) {
		fail(errs, path, "want %s", scalarText(l.want))
	}
}
