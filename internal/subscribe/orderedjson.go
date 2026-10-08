package subscribe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// jsonValue is a minimal JSON document tree that remembers object key order.
// encoding/json marshals Go maps alphabetically, which scrambles the readable
// parameter order of the client template; walking the template with this type
// keeps every object exactly as the template wrote it.
type jsonValue struct {
	kind    byte // '{' object, '[' array, 's' string, 'n' number, 'b' bool, 'z' null
	obj     []jsonMember
	arr     []*jsonValue
	str     string
	num     json.Number
	boolean bool
}

type jsonMember struct {
	key   string
	value *jsonValue
}

func (v *jsonValue) array() bool { return v != nil && v.kind == '[' }

// clone returns a deep copy, so a per-node outbound can be edited without
// mutating the shared template block two same-protocol nodes both clone.
func (v *jsonValue) clone() *jsonValue {
	if v == nil {
		return nil
	}
	out := &jsonValue{kind: v.kind, str: v.str, num: v.num, boolean: v.boolean}
	if v.obj != nil {
		out.obj = make([]jsonMember, len(v.obj))
		for i, m := range v.obj {
			out.obj[i] = jsonMember{key: m.key, value: m.value.clone()}
		}
	}
	if v.arr != nil {
		out.arr = make([]*jsonValue, len(v.arr))
		for i, e := range v.arr {
			out.arr[i] = e.clone()
		}
	}
	return out
}

// get returns the member stored under key, or nil when it is absent.
func (v *jsonValue) get(key string) *jsonValue {
	if v == nil || v.kind != '{' {
		return nil
	}
	for i := range v.obj {
		if v.obj[i].key == key {
			return v.obj[i].value
		}
	}
	return nil
}

// set replaces an existing member in place, so it keeps its position, or
// appends a new one at the end.
func (v *jsonValue) set(key string, val *jsonValue) {
	if v == nil || v.kind != '{' || key == "" {
		return
	}
	for i := range v.obj {
		if v.obj[i].key == key {
			v.obj[i].value = val
			return
		}
	}
	v.obj = append(v.obj, jsonMember{key: key, value: val})
}

func (v *jsonValue) setString(key, s string) {
	v.set(key, &jsonValue{kind: 's', str: s})
}

func (v *jsonValue) setNumber(key string, n int) {
	v.set(key, &jsonValue{kind: 'n', num: json.Number(fmt.Sprintf("%d", n))})
}

func (v *jsonValue) setStrings(key string, list []string) {
	items := make([]*jsonValue, 0, len(list))
	for _, s := range list {
		items = append(items, &jsonValue{kind: 's', str: s})
	}
	v.set(key, &jsonValue{kind: '[', arr: items})
}

// asString reads the value as text, coercing numbers and booleans.
func (v *jsonValue) asString() string {
	if v == nil {
		return ""
	}
	switch v.kind {
	case 's':
		return v.str
	case 'n':
		return v.num.String()
	case 'b':
		if v.boolean {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// parseOrderedJSON decodes a JSON document into the order-preserving tree.
func parseOrderedJSON(data []byte) (*jsonValue, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing data")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (*jsonValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			v := &jsonValue{kind: '{'}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				v.obj = append(v.obj, jsonMember{key: key, value: val})
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return v, nil
		case '[':
			v := &jsonValue{kind: '['}
			for dec.More() {
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				v.arr = append(v.arr, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return v, nil
		}
	case string:
		return &jsonValue{kind: 's', str: t}, nil
	case json.Number:
		return &jsonValue{kind: 'n', num: t}, nil
	case bool:
		return &jsonValue{kind: 'b', boolean: t}, nil
	case nil:
		return &jsonValue{kind: 'z'}, nil
	}
	return nil, fmt.Errorf("unexpected token %v", tok)
}

// MarshalJSON emits the tree with the original object key order. json.MarshalIndent
// re-indents these bytes, so the output stays conventionally formatted.
func (v *jsonValue) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	if err := v.encode(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (v *jsonValue) encode(buf *bytes.Buffer) error {
	if v == nil {
		buf.WriteString("null")
		return nil
	}
	switch v.kind {
	case '{':
		buf.WriteByte('{')
		for i, m := range v.obj {
			if i > 0 {
				buf.WriteByte(',')
			}
			key, err := json.Marshal(m.key)
			if err != nil {
				return err
			}
			buf.Write(key)
			buf.WriteByte(':')
			if err := m.value.encode(buf); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case '[':
		buf.WriteByte('[')
		for i, e := range v.arr {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := e.encode(buf); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case 's':
		b, err := json.Marshal(v.str)
		if err != nil {
			return err
		}
		buf.Write(b)
	case 'n':
		buf.WriteString(v.num.String())
	case 'b':
		if v.boolean {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	default:
		buf.WriteString("null")
	}
	return nil
}
