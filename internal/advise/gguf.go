package advise

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
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
const maxSplitGGUFShards = 4096
const maxSplitGGUFStatWorkers = 32

const (
	maxMetadataBytes      uint64 = 256 << 20
	maxArrayStorageBytes  uint64 = 64 << 20
	maxStringArrayEntries uint64 = 2 << 20
	metadataKVBytes       uint64 = 64
	interfaceBytes        uint64 = 2 * (strconv.IntSize / 8)
)

var splitGGUFName = regexp.MustCompile(`(?i)^(.*)-([0-9]{5})-of-([0-9]{5})(\.gguf)$`)

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
	size, err = completeGGUFSize(path, st)
	if err != nil {
		return nil, 0, err
	}
	return kvs, size, nil
}

func completeGGUFSize(path string, selected os.FileInfo) (int64, error) {
	match := splitGGUFName.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return selected.Size(), nil
	}
	shard, shardErr := strconv.Atoi(match[2])
	total, totalErr := strconv.Atoi(match[3])
	if shardErr != nil || totalErr != nil || total < 1 || shard < 1 || shard > total {
		return 0, fmt.Errorf("invalid split GGUF filename %q", filepath.Base(path))
	}
	if total > maxSplitGGUFShards {
		return 0, fmt.Errorf("split GGUF declares %d shards; limit is %d", total, maxSplitGGUFShards)
	}
	type shardStat struct {
		size int64
		err  error
	}
	results := make([]shardStat, total)
	dir := filepath.Dir(path)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(total, maxSplitGGUFStatWorkers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				name := fmt.Sprintf("%s-%05d-of-%05d%s", match[1], index, total, match[4])
				info, err := os.Stat(filepath.Join(dir, name))
				if err != nil {
					results[index-1].err = fmt.Errorf("incomplete split GGUF, shard %d of %d: %w", index, total, err)
					continue
				}
				if !info.Mode().IsRegular() {
					results[index-1].err = fmt.Errorf("split GGUF shard %d of %d is not a regular file", index, total)
					continue
				}
				if info.Size() < 0 {
					results[index-1].err = errors.New("split GGUF size overflows int64")
					continue
				}
				results[index-1].size = info.Size()
			}
		}()
	}
	for index := 1; index <= total; index++ {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	var size int64
	for _, result := range results {
		if result.err != nil {
			return 0, result.err
		}
		if size > math.MaxInt64-result.size {
			return 0, errors.New("split GGUF size overflows int64")
		}
		size += result.size
	}
	return size, nil
}

func ReadMetadata(r io.Reader) (map[string]any, error) {
	kvs, err := readMetadata(r, maxMetadataBytes)
	if err != nil {
		// All-or-nothing, deliberately: a short GGUF on disk is a damaged
		// GGUF. ReadMetadataPrefix is the variant for a bounded read.
		return nil, err
	}
	return kvs, nil
}

// ErrMetadataTruncated marks a header that ran out of bytes mid-decode rather
// than one that was malformed. The distinction is the whole point: a truncated
// read is an expected, benign outcome; a corrupt one is not, and they must not
// share an error path.
var ErrMetadataTruncated = errors.New("GGUF metadata ended early")

// ReadMetadataPrefix decodes as much of a GGUF header as the reader supplies
// and returns what it got, with ErrMetadataTruncated when the bytes ran out.
//
// It exists so a candidate can be sized without downloading it. Every key the
// fit math needs -- block_count, head counts, key/value length, context length,
// embedding length, and the MoE expert fields -- sits within the first two
// kilobytes of every text-generation architecture tested. The tokenizer vocab
// array comes after them and is megabytes long, so a bounded HTTP range read
// gets the architecture and nothing else, and a 16 GB model costs 4 KiB to
// size.
//
// ReadMetadata discards everything on truncation, which is right for a local
// file: a short GGUF on disk is a damaged GGUF. It is wrong for a deliberately
// bounded read, where stopping early is the plan.
//
// The returned map is still hostile input. It goes through the same decoder,
// the same budget, and the same duplicate-key and dimension checks, and a
// caller must treat missing keys as unmeasured rather than as zero -- which is
// what ArchFromKVs already does.
func ReadMetadataPrefix(r io.Reader) (map[string]any, error) {
	kvs, err := readMetadata(r, maxMetadataBytes)
	if err == nil {
		return kvs, nil
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		if len(kvs) == 0 {
			return nil, fmt.Errorf("%w before any key decoded", ErrMetadataTruncated)
		}
		return kvs, fmt.Errorf("%w after %d keys", ErrMetadataTruncated, len(kvs))
	}
	return nil, err
}

type metadataDecoder struct {
	r         io.Reader
	remaining uint64
}

func readMetadata(r io.Reader, budget uint64) (map[string]any, error) {
	d := &metadataDecoder{r: r, remaining: budget}
	var magic [4]byte
	if _, err := io.ReadFull(d.r, magic[:]); err != nil {
		return nil, err
	}
	if string(magic[:]) != ggufMagic {
		return nil, errors.New("not a GGUF file")
	}
	var version uint32
	if err := binary.Read(d.r, binary.LittleEndian, &version); err != nil {
		return nil, err
	}
	if version < 2 || version > 3 {
		return nil, fmt.Errorf("unsupported GGUF version %d", version)
	}
	var tensorCount, kvCount uint64
	if err := binary.Read(d.r, binary.LittleEndian, &tensorCount); err != nil {
		return nil, err
	}
	if err := binary.Read(d.r, binary.LittleEndian, &kvCount); err != nil {
		return nil, err
	}
	if kvCount > maxKVs {
		return nil, fmt.Errorf("GGUF metadata has %d keys; refusing", kvCount)
	}
	if err := d.chargeProduct(kvCount, metadataKVBytes, "metadata map"); err != nil {
		return nil, err
	}
	kvs := make(map[string]any, kvCount)
	for i := range kvCount {
		key, err := d.readString()
		if err != nil {
			// Partial map, not nil: ReadMetadataPrefix needs the keys already
			// decoded, and ReadMetadata discards them at its own boundary.
			return kvs, fmt.Errorf("kv %d key: %w", i, err)
		}
		if _, exists := kvs[key]; exists {
			return kvs, fmt.Errorf("duplicate GGUF metadata key %q", key)
		}
		v, err := d.readValue()
		if err != nil {
			return kvs, fmt.Errorf("kv %q: %w", key, err)
		}
		kvs[key] = v
	}
	return kvs, nil
}

func (d *metadataDecoder) charge(n uint64, purpose string) error {
	if n > d.remaining {
		return fmt.Errorf("GGUF metadata budget exceeded while decoding %s", purpose)
	}
	d.remaining -= n
	return nil
}

func (d *metadataDecoder) chargeProduct(count, size uint64, purpose string) error {
	if size != 0 && count > ^uint64(0)/size {
		return fmt.Errorf("GGUF metadata size overflows while decoding %s", purpose)
	}
	return d.charge(count*size, purpose)
}

func (d *metadataDecoder) readString() (string, error) {
	var n uint64
	if err := binary.Read(d.r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > maxStr {
		return "", fmt.Errorf("string length %d", n)
	}
	// The byte slice and resulting Go string coexist during conversion.
	if err := d.chargeProduct(n, 2, "string data"); err != nil {
		return "", err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func (d *metadataDecoder) readValue() (any, error) {
	var typ uint32
	if err := binary.Read(d.r, binary.LittleEndian, &typ); err != nil {
		return nil, err
	}
	return d.readTyped(typ)
}

func (d *metadataDecoder) readTyped(typ uint32) (any, error) {
	switch typ {
	case ggufUint8:
		var v uint8
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return uint64(v), err
	case ggufInt8:
		var v int8
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return int64(v), err
	case ggufUint16:
		var v uint16
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return uint64(v), err
	case ggufInt16:
		var v int16
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return int64(v), err
	case ggufUint32:
		var v uint32
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return uint64(v), err
	case ggufInt32:
		var v int32
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return int64(v), err
	case ggufFloat32:
		var v float32
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return float64(v), err
	case ggufBool:
		var v uint8
		if err := binary.Read(d.r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		if v > 1 {
			return nil, fmt.Errorf("invalid GGUF boolean value %d", v)
		}
		return v == 1, nil
	case ggufString:
		return d.readString()
	case ggufUint64:
		var v uint64
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return v, err
	case ggufInt64:
		var v int64
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return v, err
	case ggufFloat64:
		var v float64
		err := binary.Read(d.r, binary.LittleEndian, &v)
		return v, err
	case ggufArray:
		var etype uint32
		var n uint64
		if err := binary.Read(d.r, binary.LittleEndian, &etype); err != nil {
			return nil, err
		}
		if err := binary.Read(d.r, binary.LittleEndian, &n); err != nil {
			return nil, err
		}
		if etype == ggufArray {
			return nil, errors.New("nested GGUF arrays are not supported")
		}
		storageBytes, err := arrayElementStorageBytes(etype)
		if err != nil {
			return nil, err
		}
		limit := maxArrayStorageBytes / storageBytes
		if etype == ggufString && limit > maxStringArrayEntries {
			limit = maxStringArrayEntries
		}
		if n > limit {
			return nil, fmt.Errorf("array length %d exceeds limit %d for GGUF type %d", n, limit, etype)
		}
		if err := d.chargeProduct(n, storageBytes, "array storage"); err != nil {
			return nil, err
		}
		out := make([]any, 0, n)
		for i := range n {
			v, err := d.readTyped(etype)
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", i, err)
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown GGUF type %d", typ)
	}
}

func arrayElementStorageBytes(typ uint32) (uint64, error) {
	var valueBytes uint64
	switch typ {
	case ggufUint8, ggufInt8, ggufBool:
		valueBytes = 1
	case ggufUint16, ggufInt16:
		valueBytes = 2
	case ggufUint32, ggufInt32, ggufFloat32:
		valueBytes = 4
	case ggufUint64, ggufInt64, ggufFloat64:
		valueBytes = 8
	case ggufString:
		valueBytes = interfaceBytes
	default:
		return 0, fmt.Errorf("unknown GGUF array element type %d", typ)
	}
	return interfaceBytes + valueBytes, nil
}
