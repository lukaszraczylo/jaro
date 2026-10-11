package jaro

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
	"unsafe"
)

// Kind is the type of a Node.
type Kind uint8

// Node kinds.
const (
	KindNull Kind = iota
	KindBool
	KindInt
	KindFloat
	KindString
	KindArray
	KindObject
)

// anode is 32 bytes. Strings are spans of the input copy, or of Doc.extra when
// the high bit of ss is set. Keys are indexes into Doc.keys.
type anode struct {
	num  int64  // int value, float bits, or bool
	off  uint32 // first child in Doc.kids, or string start
	n    uint32 // child count, or string end
	key  uint32
	kind Kind
}

const extraBit = 1 << 31

// Doc is a decoded text document held in one flat node array. It allocates a
// few slices instead of a map and a boxed value per member.
type Doc struct {
	ids   map[string]uint32
	src   string
	extra string
	keys  []string
	nodes []anode
	kids  []int32
	tmp   []int32
	buf   []byte
}

func (d *Doc) text(ss, se uint32) string {
	if ss&extraBit != 0 {
		return d.extra[ss&^extraBit : se]
	}
	return d.src[ss:se]
}

// Node is a read-only view of one value in a Doc.
type Node struct {
	doc *Doc
	i   int32
}

// Root returns the top-level value.
func (d *Doc) Root() Node { return Node{d, 0} }

func (n Node) n() *anode { return &n.doc.nodes[n.i] }

// Kind returns the value type.
func (n Node) Kind() Kind { return n.n().kind }

// Len returns the member count of an array or object.
func (n Node) Len() int {
	if a := n.n(); a.kind == KindArray || a.kind == KindObject {
		return int(a.n)
	}
	return 0
}

// Index returns element i of an array or the i-th member of an object.
func (n Node) Index(i int) Node {
	if i < 0 || i >= n.Len() {
		panic("jaro: Node.Index out of range")
	}
	return Node{n.doc, n.doc.kids[int(n.n().off)+i]}
}

// Key returns the member name for a node that is an object member.
func (n Node) Key() string { return n.doc.keys[n.n().key] }

// Get returns the member of an object with the given key.
func (n Node) Get(key string) (Node, bool) {
	a := n.n()
	if a.kind != KindObject {
		return Node{}, false
	}
	for _, k := range n.doc.kids[a.off : a.off+a.n] {
		if n.doc.keys[n.doc.nodes[k].key] == key {
			return Node{n.doc, k}, true
		}
	}
	return Node{}, false
}

// String returns the value of a string node.
func (n Node) String() string { a := n.n(); return n.doc.text(a.off, a.n) }

// Int returns the value of an integer node.
func (n Node) Int() int64 { return n.n().num }

// Float returns the value of a float or integer node.
func (n Node) Float() float64 {
	a := n.n()
	if a.kind == KindInt {
		return float64(a.num)
	}
	return math.Float64frombits(uint64(a.num)) //nolint:gosec // bit pattern round trip
}

// Bool returns the value of a bool node.
func (n Node) Bool() bool { return n.n().num != 0 }

type arenaDec struct {
	*textDec
	doc  *Doc
	tmp  []int32 // child indexes of the containers being parsed
	ids  map[string]uint32
	mark []uint32        // per key id: gen of the last object that held it
	ext  strings.Builder // unescaped strings, moved to Doc.extra at the end
	gen  uint32
}

// Parse decodes JARO text into a Doc without building maps or boxed values.
// Anchors and spreads are not supported.
func Parse(data []byte) (*Doc, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("jaro: input is not valid UTF-8")
	}
	d := new(Doc)
	if err := d.parse(data, string(data)); err != nil {
		return nil, err
	}
	return d, nil
}

// Reparse replaces the Doc's content with the decoded data, reusing the
// buffers of the previous parse. Nodes and strings taken before the call are
// invalid afterwards, because strings are views of the Doc's input copy.
// Use it in a loop that parses many documents to avoid the allocations and
// garbage-collection work of Parse. On error the Doc is empty.
func (d *Doc) Reparse(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("jaro: input is not valid UTF-8")
	}
	d.buf = append(d.buf[:0], data...)
	// d.buf is not written again until the next Reparse, so the view is stable.
	str := unsafe.String(unsafe.SliceData(d.buf), len(d.buf)) //nolint:gosec // see above
	return d.parse(d.buf, str)
}

func (d *Doc) parse(data []byte, str string) error {
	td := &textDec{s: data, str: str, budget: maxExpand}
	n := min(len(data)/12, bytes.Count(data, []byte{' '})+bytes.Count(data, []byte{'\n'})) + 16
	if cap(d.nodes) < n {
		d.nodes = make([]anode, 0, n)
	}
	if cap(d.kids) < n {
		d.kids = make([]int32, 0, n)
	}
	clear(d.keys)
	d.src, d.extra = str, ""
	d.nodes, d.kids, d.keys = d.nodes[:0], d.kids[:0], d.keys[:0]
	if d.ids == nil {
		d.ids = map[string]uint32{}
	}
	clear(d.ids)
	a := &arenaDec{textDec: td, ids: d.ids, tmp: d.tmp[:0], doc: d}
	err := a.run()
	d.tmp = a.tmp[:0]
	if err != nil {
		d.nodes, d.kids, d.keys, d.src = d.nodes[:0], d.kids[:0], d.keys[:0], ""
		return err
	}
	d.extra = a.ext.String()
	return nil
}

func (a *arenaDec) run() error {
	td := a.textDec
	td.ws()
	if td.pos >= len(td.s) {
		return errors.New("jaro: empty input")
	}
	root := a.add(anode{})
	var err error
	if td.atKey() {
		err = a.members(root, 0, 0)
	} else {
		err = a.value(root, 0)
	}
	if err != nil {
		return err
	}
	td.ws()
	if td.pos != len(td.s) {
		return td.errf("trailing data")
	}
	return nil
}

func (a *arenaDec) add(n anode) int32 {
	a.doc.nodes = append(a.doc.nodes, n)
	return int32(len(a.doc.nodes) - 1) //nolint:gosec // bounded by input size
}

// seal copies the child indexes pushed since start into the node's kid range.
func (a *arenaDec) seal(at int32, start int, kind Kind) {
	kids := a.tmp[start:]
	nd := &a.doc.nodes[at]
	nd.kind = kind
	nd.off = uint32(len(a.doc.kids)) //nolint:gosec // bounded by input size
	nd.n = uint32(len(kids))         //nolint:gosec // bounded by input size
	a.doc.kids = append(a.doc.kids, kids...)
	a.tmp = a.tmp[:start]
}

func (a *arenaDec) value(at int32, depth int) error {
	d := a.textDec
	if depth > maxDepth {
		return d.errf("nesting too deep")
	}
	d.ws()
	if d.pos >= len(d.s) {
		return d.errf("unexpected end")
	}
	switch d.s[d.pos] {
	case '&', '*':
		return d.errf("anchors are not supported by Parse")
	case '{':
		d.pos++
		if d.ws(); d.pos < len(d.s) && d.s[d.pos] == '|' {
			d.pos++
			return a.keyed(at, depth)
		}
		return a.members(at, depth, '}')
	case '[':
		d.pos++
		return a.array(at, depth)
	case '}', ']', '|', ':':
		return d.errf("unexpected %q", d.s[d.pos])
	}
	return a.scalar(at)
}

func (a *arenaDec) members(at int32, depth int, end byte) error {
	d := a.textDec
	start := len(a.tmp)
	for {
		d.ws()
		if d.pos >= len(d.s) {
			if end == 0 {
				if err := a.uniqueKids(start); err != nil {
					return err
				}
				a.seal(at, start, KindObject)
				return nil
			}
			return d.errf("unterminated object")
		}
		if end != 0 && d.s[d.pos] == end {
			d.pos++
			if err := a.uniqueKids(start); err != nil {
				return err
			}
			a.seal(at, start, KindObject)
			return nil
		}
		key, err := a.key()
		if err != nil {
			return err
		}
		m := a.add(anode{key: key})
		if err = a.value(m, depth+1); err != nil {
			return err
		}
		a.tmp = append(a.tmp, m)
	}
}

// uniqueKids rejects repeated member names among the children pushed since start.
func (a *arenaDec) uniqueKids(start int) error {
	kids := a.tmp[start:]
	if len(kids) <= 8 {
		for i, k := range kids {
			for _, p := range kids[:i] {
				if a.doc.nodes[k].key == a.doc.nodes[p].key {
					return a.errf("duplicate key %q", a.doc.keys[a.doc.nodes[k].key])
				}
			}
		}
		return nil
	}
	if len(a.mark) < len(a.doc.keys) {
		a.mark = append(a.mark, make([]uint32, len(a.doc.keys)-len(a.mark))...)
	}
	a.gen++
	for _, k := range kids {
		id := a.doc.nodes[k].key
		if a.mark[id] == a.gen {
			return a.errf("duplicate key %q", a.doc.keys[id])
		}
		a.mark[id] = a.gen
	}
	return nil
}

// intern returns the key table index of k.
func (a *arenaDec) intern(k string) uint32 {
	if id, ok := a.ids[k]; ok {
		return id
	}
	id := uint32(len(a.doc.keys)) //nolint:gosec // bounded by input size
	a.doc.keys = append(a.doc.keys, k)
	a.ids[k] = id
	return id
}

// resolve fills the key table index of every column in specs.
func (a *arenaDec) resolve(specs []colSpec) {
	for i := range specs {
		specs[i].id = a.intern(specs[i].key)
		a.resolve(specs[i].sub)
	}
}

// key reads "name:" and leaves the position on the value.
func (a *arenaDec) key() (uint32, error) {
	d := a.textDec
	var name string
	if d.s[d.pos] == '"' {
		v, err := d.quoted()
		if err != nil {
			return 0, err
		}
		name = v.(string)
	} else {
		start, b := d.pos, d.s
		p := start
		for p < len(b) && !delimTable[b[p]] {
			p++
		}
		name = d.str[start:p]
		d.pos = p
		if name == "" || name == "~" || name == "true" || name == "false" || numLike(name) {
			return 0, d.errf("expected key:")
		}
	}
	if d.pos >= len(d.s) || d.s[d.pos] != ':' {
		return 0, d.errf("expected key:")
	}
	d.pos++
	return a.intern(name), nil
}

func (a *arenaDec) array(at int32, depth int) error {
	d := a.textDec
	d.ws()
	if d.pos < len(d.s) && d.s[d.pos] == '|' {
		d.pos++
		specs, err := d.specList('|', 0)
		if err != nil {
			return err
		}
		a.resolve(specs)
		return a.rows(at, depth, specs)
	}
	start := len(a.tmp)
	for {
		d.ws()
		if d.pos >= len(d.s) {
			return d.errf("unterminated array")
		}
		if d.s[d.pos] == ']' {
			d.pos++
			a.seal(at, start, KindArray)
			return nil
		}
		e := a.add(anode{})
		if err := a.value(e, depth+1); err != nil {
			return err
		}
		a.tmp = append(a.tmp, e)
	}
}

func (a *arenaDec) rows(at int32, depth int, specs []colSpec) error {
	d := a.textDec
	start := len(a.tmp)
	for {
		d.ws()
		if d.pos >= len(d.s) {
			return d.errf("unterminated array")
		}
		if d.s[d.pos] == ']' {
			d.pos++
			a.seal(at, start, KindArray)
			return nil
		}
		row := a.add(anode{})
		if err := a.cells(row, specs, depth, ']'); err != nil {
			return err
		}
		a.tmp = append(a.tmp, row)
	}
}

// cells reads one object from the leaf columns of specs into node at.
func (a *arenaDec) cells(at int32, specs []colSpec, depth int, closer byte) error {
	d := a.textDec
	start := len(a.tmp)
	for _, s := range specs {
		if s.sub != nil {
			m := a.add(anode{key: s.id})
			if err := a.cells(m, s.sub, depth, closer); err != nil {
				return err
			}
			a.tmp = append(a.tmp, m)
			continue
		}
		d.ws()
		if d.pos >= len(d.s) {
			return d.errf("unterminated table")
		}
		if d.s[d.pos] == closer {
			return d.errf("%v", errRowIncomplete)
		}
		if d.s[d.pos] == absentCell && (d.pos+1 >= len(d.s) || isDelim(d.s[d.pos+1])) {
			d.pos++
			continue
		}
		m := a.add(anode{key: s.id})
		if err := a.value(m, depth+1); err != nil {
			return err
		}
		a.tmp = append(a.tmp, m)
	}
	a.seal(at, start, KindObject)
	return nil
}

func (a *arenaDec) keyed(at int32, depth int) error {
	d := a.textDec
	specs, err := d.specList('|', 0)
	if err != nil {
		return err
	}
	a.resolve(specs)
	start := len(a.tmp)
	for {
		d.ws()
		if d.pos >= len(d.s) {
			return d.errf("unterminated object")
		}
		if d.s[d.pos] == '}' {
			d.pos++
			if err := a.uniqueKids(start); err != nil {
				return err
			}
			a.seal(at, start, KindObject)
			return nil
		}
		k, err := d.token()
		if err != nil {
			return err
		}
		name, ok := k.(string)
		if !ok {
			return d.errf("expected row name")
		}
		row := a.add(anode{key: a.intern(name)})
		if err = a.cells(row, specs, depth, '}'); err != nil {
			return err
		}
		a.tmp = append(a.tmp, row)
	}
}

func (a *arenaDec) scalar(at int32) error {
	d := a.textDec
	nd := &a.doc.nodes[at]
	if d.s[d.pos] == '"' {
		return a.quotedString(at)
	}
	start, b := d.pos, d.s
	p := start
	for p < len(b) && !delimTable[b[p]] {
		p++
	}
	if p == start {
		return d.errf("unexpected %q", b[p])
	}
	d.pos = p
	tok := d.str[start:p]
	switch tok {
	case "~":
		nd.kind = KindNull
		return nil
	case "true", "false":
		nd.kind = KindBool
		if tok == "true" {
			nd.num = 1
		}
		return nil
	}
	if i, f, isInt, ok := fastNum(b[start:p]); ok {
		if isInt {
			nd.kind, nd.num = KindInt, i
		} else {
			nd.kind, nd.num = KindFloat, int64(math.Float64bits(f)) //nolint:gosec // bit pattern
		}
		return nil
	}
	if numLike(tok) && isNumber(tok) {
		if isIntToken(tok) {
			if i, err := strconv.ParseInt(tok, 10, 64); err == nil {
				nd.kind, nd.num = KindInt, i
				return nil
			}
		}
		if f, err := strconv.ParseFloat(tok, 64); err == nil && !math.IsInf(f, 0) {
			if f == 0 { // match Unmarshal: zero is always an integer
				nd.kind = KindInt
				return nil
			}
			nd.kind, nd.num = KindFloat, int64(math.Float64bits(f)) //nolint:gosec // bit pattern
			return nil
		}
	}
	nd.kind, nd.off, nd.n = KindString, uint32(start), uint32(p) //nolint:gosec // bounded by input size
	return nil
}

// quotedString reads a quoted string into node at, as a span of the input
// when it has no escapes.
func (a *arenaDec) quotedString(at int32) error {
	d := a.textDec
	open := d.pos
	nd := &a.doc.nodes[at]
	if end := bytes.IndexByte(d.s[open+1:], '"'); end >= 0 && bytes.IndexByte(d.s[open+1:open+1+end], '\\') < 0 {
		end += open + 1
		nd.kind, nd.off, nd.n = KindString, uint32(open+1), uint32(end) //nolint:gosec // bounded by input size
		d.pos = end + 1
		return nil
	}
	p := len(d.s)
	if i := bytes.IndexByte(d.s[open+1:], '\\'); i >= 0 {
		p = open + 1 + i
	}
	off := a.ext.Len()
	d.pos = p
	if err := d.unescape(&a.ext, open+1); err != nil {
		return err
	}
	nd = &a.doc.nodes[at]
	nd.kind, nd.off, nd.n = KindString, uint32(off)|extraBit, uint32(a.ext.Len()) //nolint:gosec // bounded by input size
	return nil
}
