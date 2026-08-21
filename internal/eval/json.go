package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// decodeBuiltinJSON rejects both schema drift and concatenated JSON. Embedded
// definitions are part of the measurement contract, so accepting a misspelled
// field or silently ignoring a second value would create a different test than
// the file appears to define.
func decodeBuiltinJSON(b []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("content after the JSON value")
		}
		return err
	}
	return nil
}
