package artifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/blisspixel/fitr/internal/strictjson"
)

func LoadSpec(path string) (Spec, error) {
	var spec Spec
	data, err := readDocument(path, MaxSpecBytes)
	if err != nil {
		return spec, err
	}
	if err := decodeDocument(data, &spec); err != nil {
		return Spec{}, err
	}
	return spec, spec.Validate()
}

// LoadBinding validates only the saved receipt. It never reopens mapped weights
// or the original source receipt, which may have moved since the observation.
func LoadBinding(path string) (Binding, error) {
	var result Binding
	data, err := readDocument(path, MaxReceiptBytes)
	if err != nil {
		return result, err
	}
	if err := decodeDocument(data, &result); err != nil {
		return Binding{}, err
	}
	return result, result.Validate()
}

func (result Binding) JSON() ([]byte, error) {
	if err := result.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data)+1 > MaxReceiptBytes {
		return nil, errors.New("artifact receipt exceeds two MiB")
	}
	return append(data, '\n'), nil
}

// WriteBinding exclusively publishes a synced private sibling temporary file.
// Hard-link publication fails closed where unsupported. No directories are
// created; no existing output or missing mapped input may be overwritten.
func WriteBinding(path string, result Binding) error {
	path, err := checkedPath(path)
	if err != nil {
		return err
	}
	data, err := result.JSON()
	if err != nil {
		return err
	}
	if err := ValidateBindingOutputPath(path, result.Mapping); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".fitr-artifact-*")
	if err != nil {
		return err
	}
	defer func() { _ = temporary.Close(); _ = os.Remove(temporary.Name()) }()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := ValidateBindingOutputPath(path, result.Mapping); err != nil {
		return err
	}
	return os.Link(temporary.Name(), path)
}

func readDocument(path string, limit int64) ([]byte, error) {
	path, err := checkedPath(path)
	if err != nil {
		return nil, err
	}
	if err := rejectLinks(path, false); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit {
		return nil, errors.New("artifact document must be a bounded regular file")
	}
	if !os.SameFile(before, before) {
		return nil, errors.New("artifact document identity could not be captured")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !sameFacts(before, opened) {
		return nil, errors.New("artifact document changed before reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != before.Size() || int64(len(data)) > limit || !sameFacts(before, after) || !sameFacts(after, pathInfo) || rejectLinks(path, false) != nil {
		return nil, errors.New("artifact document changed or exceeded its bound")
	}
	return data, nil
}

func decodeDocument(data []byte, target any) error {
	if err := strictjson.Validate(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	originalTree, err := documentTree(data)
	if err != nil {
		return err
	}
	canonicalTree, err := documentTree(canonical)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(originalTree, canonicalTree) {
		return errors.New("noncanonical artifact JSON fields")
	}
	return nil
}

func documentTree(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var tree any
	err := decoder.Decode(&tree)
	return tree, err
}
