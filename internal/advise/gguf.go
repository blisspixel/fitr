package advise

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const ggufMagic = "GGUF"

// GGUF value types. From ggml-org/ggml docs/gguf.md.
const (
	ggufUint8   uint32 = 0
	ggufInt8    uint32 = 1
	ggufUint16  uint32 = 2
	ggufInt16   uint32 = 3
	ggufUint32  uint32 = 4
	ggufInt32   uint32 = 5
	ggufFloat32 uint32 = 6
	ggufBool    uint32 = 7
	ggufString  uint32 = 8
	ggufArray   uint32 = 9
	ggufUint64  uint32 = 10
	ggufInt64   uint32 = 11
	ggufFloat64 uint32 = 12
)

const maxKVs = 4096
const maxStr = 16 << 20

// OpenGGUF reads only the metadata header of a GGUF file. The tensor payload
// is not loaded; size is the on-disk length (the weights).
func OpenGGUF(path string) (kvs map[string]any, size int64, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	kvs, err = ReadMetadata(f)
	if err != nil {
		return nil, 0, err
	}
	return kvs, st.Size(), nil
}

func ReadMetadata(r io.Reader) (map[string]any, error) {
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil, err
	}
	if string(magic[:]) != ggufMagic {
		return nil, fmt.Errorf("not a GGUF file")
	}
	var version uint32
	if err := binary.Read(r, binary.LittleEndian, &version); err != nil {
		return nil, err
	}
	if version < 2 || version > 3 {
		return nil, fmt.Errorf("unsupported GGUF version %d", version)
	}
	var tensorCount, kvCount uint64
	if err := binary.Read(r, binary.LittleEndian, &tensorCount); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &kvCount); err != nil {
		return nil, err
	}
	if kvCount > maxKVs {
		return nil, fmt.Errorf("GGUF metadata has %d keys; refusing", kvCount)
	}
	kvs := make(map[string]any, kvCount)
	for i := uint64(0); i < kvCount; i++ {
		key, err := readString(r)
		if err != nil {
			return nil, fmt.Errorf("kv %d key: %w", i, err)
		}
		v, err := readValue(r)
		if err != nil {
			return nil, fmt.Errorf("kv %q: %w", key, err)
		}
		kvs[key] = v
	}
	return kvs, nil
}

func readString(r io.Reader) (string, error) {
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > maxStr {
		return "", fmt.Errorf("string length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func readValue(r io.Reader) (any, error) {
	var typ uint32
	if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
		return nil, err
	}
	return readTyped(r, typ)
}

func readTyped(r io.Reader, typ uint32) (any, error) {
	switch typ {
	case ggufUint8:
		var v uint8
		err := binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case ggufInt8:
		var v int8
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case ggufUint16:
		var v uint16
		err := binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case ggufInt16:
		var v int16
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case ggufUint32:
		var v uint32
		err := binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case ggufInt32:
		var v int32
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case ggufFloat32:
		var v float32
		err := binary.Read(r, binary.LittleEndian, &v)
		return float64(v), err
	case ggufBool:
		var v uint8
		err := binary.Read(r, binary.LittleEndian, &v)
		return v != 0, err
	case ggufString:
		return readString(r)
	case ggufUint64:
		var v uint64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case ggufInt64:
		var v int64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case ggufFloat64:
		var v float64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case ggufArray:
		var etype uint32
		var n uint64
		if err := binary.Read(r, binary.LittleEndian, &etype); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return nil, err
		}
		if n > maxStr {
			return nil, fmt.Errorf("array length %d", n)
		}
		out := make([]any, 0, n)
		for i := uint64(0); i < n; i++ {
			v, err := readTyped(r, etype)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown GGUF type %d", typ)
	}
}
