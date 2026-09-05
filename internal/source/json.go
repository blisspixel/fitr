package source

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/blisspixel/fitr/internal/strictjson"
)

// Upstream may add fields; fitr receipts may not. Both reject case variants of
// known fields instead of relying on encoding/json's case-insensitive matching.
func decodeJSON(data []byte, target any, allowUnknown bool) error {
	if err := strictjson.Validate(data); err != nil {
		return err
	}
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		return err
	}
	if err := checkFieldNames(tree, reflect.TypeOf(target), allowUnknown); err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func checkFieldNames(tree any, typ reflect.Type, allowUnknown bool) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if tree == nil {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := tree.(map[string]any)
		if !ok {
			return errors.New("expected JSON object")
		}
		return checkObjectNames(object, typ, allowUnknown)
	case reflect.Slice:
		items, ok := tree.([]any)
		if !ok {
			return nil
		} // The typed decoder rejects wrong value types.
		for _, item := range items {
			if err := checkFieldNames(item, typ.Elem(), allowUnknown); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkObjectNames(object map[string]any, typ reflect.Type, allowUnknown bool) error {
	fields := make(map[string]reflect.Type, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		fields[name] = field.Type
	}
	for name, value := range object {
		field, known := fields[name]
		if !known {
			if !allowUnknown {
				return errors.New("unknown source JSON field")
			}
			for exact := range fields {
				if strings.EqualFold(exact, name) {
					return errors.New("incorrect source JSON field spelling")
				}
			}
			continue
		}
		if err := checkFieldNames(value, field, allowUnknown); err != nil {
			return err
		}
	}
	return nil
}
