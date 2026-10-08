// Package plist encodes Go values as XML property lists, which is all a bundle
// build needs of the format: Info.plist is written, never read.
//
// It is small on purpose. Decoding, the binary format and struct tags are the
// business of a general plist library; this covers the closed set of types an
// Info.plist holds, and refuses anything else with an error that names where it
// was found. Silently stringifying a type it did not recognise would write a
// plist that loads and means something other than what the caller wrote.
//
// Output is deterministic: dictionary keys are sorted, so the same input is the
// same bytes on every run, which is what makes a build reproducible.
package plist

import (
	"encoding/base64"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const header = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
`

// Marshal renders root as an XML property list.
//
// The values it accepts are strings, booleans, integers, finite floats,
// []byte (written as <data>), time.Time (written as <date>, in UTC), maps with
// string keys, and slices or arrays of any of those. Anything else — nil
// included, which a plist cannot express — is an error.
func Marshal(root map[string]any) ([]byte, error) {
	var b strings.Builder
	b.WriteString(header)
	if err := writeValue(&b, reflect.ValueOf(root), 0, "root"); err != nil {
		return nil, err
	}
	b.WriteString("</plist>\n")
	return []byte(b.String()), nil
}

var timeType = reflect.TypeFor[time.Time]()

func writeValue(b *strings.Builder, v reflect.Value, depth int, path string) error {
	pad := strings.Repeat("\t", depth)

	// An interface holding a value is unwrapped; one holding nothing is not
	// something a plist can say.
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return fmt.Errorf("plist: %s is nil, which a property list cannot represent", path)
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return fmt.Errorf("plist: %s is nil, which a property list cannot represent", path)
	}

	if v.Type() == timeType {
		t := v.Interface().(time.Time)
		b.WriteString(pad + "<date>" + t.UTC().Format(time.RFC3339) + "</date>\n")
		return nil
	}

	switch v.Kind() {
	case reflect.String:
		s, err := escape(v.String())
		if err != nil {
			return fmt.Errorf("plist: %s: %w", path, err)
		}
		b.WriteString(pad + "<string>" + s + "</string>\n")

	case reflect.Bool:
		if v.Bool() {
			b.WriteString(pad + "<true/>\n")
		} else {
			b.WriteString(pad + "<false/>\n")
		}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(pad + "<integer>" + strconv.FormatInt(v.Int(), 10) + "</integer>\n")

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		// A plist integer is signed 64-bit. A larger unsigned value would wrap
		// to a negative number, which is not what the caller wrote.
		if v.Uint() > math.MaxInt64 {
			return fmt.Errorf("plist: %s is %d, which does not fit a property list integer", path, v.Uint())
		}
		b.WriteString(pad + "<integer>" + strconv.FormatUint(v.Uint(), 10) + "</integer>\n")

	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("plist: %s is %v, which a property list cannot represent", path, f)
		}
		b.WriteString(pad + "<real>" + strconv.FormatFloat(f, 'g', -1, 64) + "</real>\n")

	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			// []byte is data, not an array of integers.
			raw := make([]byte, v.Len())
			reflect.Copy(reflect.ValueOf(raw), v)
			writeData(b, pad, raw)
			return nil
		}
		if v.Len() == 0 {
			b.WriteString(pad + "<array/>\n")
			return nil
		}
		b.WriteString(pad + "<array>\n")
		for i := range v.Len() {
			if err := writeValue(b, v.Index(i), depth+1, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		b.WriteString(pad + "</array>\n")

	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("plist: %s is a map keyed by %s; property list dictionaries are keyed by strings",
				path, v.Type().Key())
		}
		if v.Len() == 0 {
			b.WriteString(pad + "<dict/>\n")
			return nil
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

		b.WriteString(pad + "<dict>\n")
		for _, k := range keys {
			name, err := escape(k.String())
			if err != nil {
				return fmt.Errorf("plist: a key under %s: %w", path, err)
			}
			b.WriteString(pad + "\t<key>" + name + "</key>\n")
			if err := writeValue(b, v.MapIndex(k), depth+1, path+"."+k.String()); err != nil {
				return err
			}
		}
		b.WriteString(pad + "</dict>\n")

	default:
		return fmt.Errorf("plist: %s is a %s, which a property list cannot represent", path, v.Type())
	}
	return nil
}

// dataLine is how many base64 characters Apple puts on a line of <data>.
const dataLine = 68

// writeData writes raw as Apple's own tools do: the base64 on lines of their own,
// indented to the value's depth and wrapped at 68 characters. A reader takes
// either form, but matching the platform means a plist round-tripped through
// plutil comes back byte for byte.
func writeData(b *strings.Builder, pad string, raw []byte) {
	enc := base64.StdEncoding.EncodeToString(raw)
	if enc == "" {
		b.WriteString(pad + "<data>\n" + pad + "</data>\n")
		return
	}
	b.WriteString(pad + "<data>\n")
	for len(enc) > 0 {
		n := min(dataLine, len(enc))
		b.WriteString(pad + enc[:n] + "\n")
		enc = enc[n:]
	}
	b.WriteString(pad + "</data>\n")
}

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// escape makes s safe as XML text, or says why it cannot be.
//
// XML 1.0 forbids most control characters outright — they cannot be escaped, so
// a document containing one is not well-formed and a plist reader rejects the
// whole file. Better to say so here, naming the value, than to write an
// Info.plist that fails to load with nothing pointing at the cause.
func escape(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%q is not valid UTF-8", s)
	}
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if r < 0x20 || r == 0xFFFE || r == 0xFFFF || (r >= 0xD800 && r <= 0xDFFF) {
			return "", fmt.Errorf("%q contains U+%04X, which XML cannot carry", s, r)
		}
	}
	return escaper.Replace(s), nil
}
