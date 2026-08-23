// Package strictjson rejects ambiguous JSON before a value reaches a typed
// decoder. The standard library intentionally accepts duplicate object names
// and keeps the last value, but different JSON consumers need not agree on
// that interpretation. Decision-grade inputs and receipts must have one
// unambiguous meaning.
package strictjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const maxNestingDepth = 1000

// Validate accepts exactly one JSON value and rejects duplicate object names
// at every nesting level. Object names are compared after JSON unescaping, so
// equivalent spellings cannot bypass duplicate detection.
func Validate(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := scanValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("content after the JSON value")
		}
		return err
	}
	return nil
}

// Unmarshal is json.Unmarshal with duplicate-name and single-value checks.
func Unmarshal(data []byte, into any) error {
	if err := Validate(data); err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

func scanValue(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, composite := tok.(json.Delim)
	if !composite {
		return nil
	}
	if depth >= maxNestingDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxNestingDepth)
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			nameToken, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("JSON object name has type %T", nameToken)
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate JSON object name %q", name)
			}
			seen[name] = struct{}{}
			if err := scanValue(dec, depth+1); err != nil {
				if err == io.EOF {
					return io.ErrUnexpectedEOF
				}
				return err
			}
		}
		return consumeClosing(dec, '}')
	case '[':
		for dec.More() {
			if err := scanValue(dec, depth+1); err != nil {
				if err == io.EOF {
					return io.ErrUnexpectedEOF
				}
				return err
			}
		}
		return consumeClosing(dec, ']')
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func consumeClosing(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	if err != nil {
		return err
	}
	if tok != want {
		return fmt.Errorf("unexpected JSON delimiter %v, want %q", tok, want)
	}
	return nil
}
