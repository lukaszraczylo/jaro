package jaro

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// FromJSON decodes JSON into JARO's data model, keeping integers exact.
func FromJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return fixNumbers(v), nil
}

func fixNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return i
		}
		f, _ := strconv.ParseFloat(string(x), 64)
		return f
	case map[string]any:
		for k, el := range x {
			x[k] = fixNumbers(el)
		}
	case []any:
		for i, el := range x {
			x[i] = fixNumbers(el)
		}
	}
	return v
}

// ToJSON encodes JARO data as compact JSON.
func ToJSON(v any) ([]byte, error) { return json.Marshal(v) }
