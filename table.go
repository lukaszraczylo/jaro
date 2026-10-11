package jaro

import (
	"errors"
	"sort"
)

const maxNest = 8 // deepest nesting of object columns in one table header

// colSpec is one table column. A column with sub-columns holds an object in
// every row, written as one cell per sub-column.
type colSpec struct {
	key string
	sub []colSpec
	id  uint32 // key table index, set by the arena decoder
}

// colInfo gathers what buildSpecs learns about one key in a single pass.
type colInfo struct {
	objs []any // the object under the key in each row
	n    int   // rows that have the key
	flat bool  // some row holds a value that is not an object
}

// buildSpecs returns the columns for rows, which must all be objects, and
// whether enough cells are present for a table.
func buildSpecs(rows []any, depth int) ([]colSpec, bool) {
	info := map[string]*colInfo{}
	present := 0
	for _, r := range rows {
		m := r.(map[string]any)
		present += len(m)
		for k, v := range m {
			c := info[k]
			if c == nil {
				c = &colInfo{}
				info[k] = c
			}
			c.n++
			if o, ok := v.(map[string]any); ok {
				c.objs = append(c.objs, o)
			} else {
				c.flat = true
			}
		}
	}
	if len(info) == 0 || float64(present) < textPresent*float64(len(info)*len(rows)) {
		return nil, false
	}
	keys := make([]string, 0, len(info))
	for k := range info {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	specs := make([]colSpec, len(keys))
	for i, k := range keys {
		specs[i].key = k
		if c := info[k]; depth < maxNest && !c.flat && c.n == len(rows) {
			if sub, ok := buildSpecs(c.objs, depth+1); ok && len(sub) >= 2 {
				specs[i].sub = sub
			}
		}
	}
	return specs, true
}

// tableSpecs reports whether rows can be written as a table and returns its columns.
func tableSpecs(rows []any) ([]colSpec, bool) {
	if len(rows) < 2 {
		return nil, false
	}
	for _, r := range rows {
		if _, ok := r.(map[string]any); !ok {
			return nil, false
		}
	}
	return buildSpecs(rows, 0)
}

func (e *textEnc) specHeader(specs []colSpec) {
	for i, s := range specs {
		if i > 0 {
			e.buf = append(e.buf, ' ')
		}
		e.str(s.key)
		if s.sub != nil {
			e.buf = append(e.buf, '{')
			e.specHeader(s.sub)
			e.buf = append(e.buf, '}')
		}
	}
}

// specCells writes one cell per leaf column of row, each led by a space.
func (e *textEnc) specCells(specs []colSpec, row map[string]any, depth int) error {
	for _, s := range specs {
		v, present := row[s.key]
		switch {
		case s.sub != nil:
			if err := e.specCells(s.sub, v.(map[string]any), depth); err != nil {
				return err
			}
		case !present:
			e.buf = append(e.buf, ' ', absentCell)
		default:
			e.buf = append(e.buf, ' ')
			if err := e.value(v, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// table writes the "|header|" and the cells of rows.
func (e *textEnc) table(rows []any, specs []colSpec, depth int) error {
	e.buf = append(e.buf, '|')
	e.specHeader(specs)
	e.buf = append(e.buf, '|')
	for _, r := range rows {
		if err := e.specCells(specs, r.(map[string]any), depth); err != nil {
			return err
		}
	}
	return nil
}

// keyedTable writes an object of row objects as {|header| name cell...}.
func (e *textEnc) keyedTable(m map[string]any, specs []colSpec, depth int) error {
	e.buf = append(e.buf, '{', '|')
	e.specHeader(specs)
	e.buf = append(e.buf, '|')
	start := len(e.ks)
	e.ks = appendSortedKeys(e.ks, m)
	for _, name := range e.ks[start:] {
		e.buf = append(e.buf, ' ')
		e.str(name)
		if err := e.specCells(specs, m[name].(map[string]any), depth); err != nil {
			return err
		}
	}
	e.ks = e.ks[:start]
	e.buf = append(e.buf, '}')
	return nil
}

// keyedSpecs returns the columns when every value of m is an object, m has at
// least two entries, and a table of them has at least two keys.
func keyedSpecs(m map[string]any) ([]colSpec, bool) {
	if len(m) < 2 {
		return nil, false
	}
	rows := make([]any, 0, len(m))
	for _, v := range m {
		if _, ok := v.(map[string]any); !ok {
			return nil, false
		}
		rows = append(rows, v)
	}
	specs, ok := buildSpecs(rows, 0)
	if !ok || len(specs) < 2 {
		return nil, false
	}
	return specs, true
}

// specList reads column keys up to end, which it consumes. A key followed by
// '{' has sub-columns.
func (d *textDec) specList(end byte, depth int) ([]colSpec, error) {
	if depth > maxNest {
		return nil, d.errf("table header nested too deep")
	}
	var specs []colSpec
	for {
		d.ws()
		if d.pos >= len(d.s) {
			return nil, d.errf("unterminated table header")
		}
		if d.s[d.pos] == end {
			d.pos++
			break
		}
		k, err := d.token()
		if err != nil {
			return nil, err
		}
		ks, ok := k.(string)
		if !ok {
			return nil, d.errf("table key must be a string")
		}
		spec := colSpec{key: ks}
		if d.ws(); d.pos < len(d.s) && d.s[d.pos] == '{' {
			d.pos++
			if spec.sub, err = d.specList('}', depth+1); err != nil {
				return nil, err
			}
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, d.errf("empty table header")
	}
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.key
	}
	if k, dup := duplicate(names); dup {
		return nil, d.errf("duplicate table key %q", k)
	}
	return specs, nil
}

var errRowIncomplete = errors.New("table row is incomplete")

// readRow reads one cell per leaf column into row. closer ends the table, so
// meeting it mid-row means the cell count is wrong.
func (d *textDec) readRow(specs []colSpec, row map[string]any, depth int, closer byte) error {
	for _, s := range specs {
		if s.sub != nil {
			obj := make(map[string]any, len(s.sub))
			if err := d.readRow(s.sub, obj, depth, closer); err != nil {
				return err
			}
			row[s.key] = obj
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
		v, err := d.value(depth + 1)
		if err != nil {
			return err
		}
		row[s.key] = v
	}
	return nil
}

// keyedTable reads {|header| name cell... name cell...}. The opening "{|" is consumed.
func (d *textDec) keyedTable(depth int) (any, error) {
	specs, err := d.specList('|', 0)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for {
		d.ws()
		if d.pos >= len(d.s) {
			return nil, d.errf("unterminated object")
		}
		if d.s[d.pos] == '}' {
			d.pos++
			return out, nil
		}
		k, err := d.token()
		if err != nil {
			return nil, err
		}
		name, ok := k.(string)
		if !ok {
			return nil, d.errf("expected row name")
		}
		row := make(map[string]any, len(specs))
		if err := d.readRow(specs, row, depth, '}'); err != nil {
			return nil, err
		}
		before := len(out)
		out[name] = row
		if len(out) == before {
			return nil, d.errf("duplicate row name %q", name)
		}
	}
}

// duplicate returns the first repeated name.
func duplicate(names []string) (string, bool) {
	if len(names) <= 8 {
		for i, a := range names {
			for _, b := range names[:i] {
				if a == b {
					return a, true
				}
			}
		}
		return "", false
	}
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		if _, ok := seen[n]; ok {
			return n, true
		}
		seen[n] = struct{}{}
	}
	return "", false
}

// tableRows reads rows until ']'. The opening "[|" is consumed.
func (d *textDec) tableRows(depth int, specs []colSpec) (any, error) {
	rows := []any{}
	for {
		d.ws()
		if d.pos >= len(d.s) {
			return nil, d.errf("unterminated array")
		}
		if d.s[d.pos] == ']' {
			d.pos++
			return rows, nil
		}
		row := make(map[string]any, len(specs))
		if err := d.readRow(specs, row, depth, ']'); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
}
