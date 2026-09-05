package autoruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/blisspixel/fitr/internal/strictjson"
)

const MaxShowBytes = 4 << 20

type ModelConfiguration struct {
	Model  string `json:"model"`
	SHA256 string `json:"sha256"`
}

func (m ModelConfiguration) Validate() error {
	if !validModel(m.Model) || !digestPattern.MatchString(m.SHA256) {
		return errors.New("invalid owned model configuration identity")
	}
	return nil
}

func validModel(model string) bool {
	if len(model) == 0 || len(model) > 512 || strings.TrimSpace(model) != model {
		return false
	}
	for _, c := range model {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// ModelConfiguration reads declared behavior from this owned endpoint. It does
// not load weights or verify extra adapter/projector files named by Modelfile.
func (r *Runtime) ModelConfiguration(ctx context.Context, model string) (ModelConfiguration, error) {
	if !validModel(model) {
		return ModelConfiguration{}, errors.New("invalid owned model name")
	}
	body, _ := json.Marshal(map[string]string{"model": model})
	data, err := r.readMetadata(ctx, http.MethodPost, "/api/show", body, MaxShowBytes)
	if err != nil {
		return ModelConfiguration{}, err
	}
	digest, err := ModelConfigurationSHA256(data)
	if err != nil {
		return ModelConfiguration{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ModelConfiguration{}, errors.New("owned runtime closed during model inspection")
	}
	if !r.configurations[digest] && len(r.configurations) >= 32 {
		return ModelConfiguration{}, errors.New("owned runtime model configuration limit reached")
	}
	r.configurations[digest] = true
	return ModelConfiguration{Model: model, SHA256: digest}, nil
}

// ModelConfigurationSHA256 seals the complete recognized Show behavior fields.
// The raw declaration is parsed with numbers preserved and duplicate keys
// refused. Unknown top-level fields require review before this profile can use
// them. Identity, modification time and license do not change behavior.
func ModelConfigurationSHA256(data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxShowBytes {
		return "", errors.New("model Show response exceeds its bounded size")
	}
	if err := strictjson.Validate(data); err != nil {
		return "", err
	}
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return "", err
	}
	if fields == nil {
		return "", errors.New("model Show response must be an object")
	}
	if err := validateShowFields(fields); err != nil {
		return "", err
	}
	for _, ignored := range []string{"license", "modified_at", "name", "size", "digest"} {
		delete(fields, ignored)
	}
	return sealJSON("fitr.autoruntime.model-configuration.v1", fields), nil
}

var draftParameter = regexp.MustCompile(`(?im)^\s*(?:draft[^\s]*|num_draft|mtp[^\s]*)\s+`)
var extraComponent = regexp.MustCompile(`(?im)^\s*(?:adapter|projector|draft)\s+`)

func validateShowFields(fields map[string]any) error {
	for key, value := range fields {
		if err := validateShowField(key, value); err != nil {
			return err
		}
	}
	info, ok := fields["model_info"].(map[string]any)
	if !ok || len(info) == 0 {
		return errors.New("owned model configuration requires nonempty model_info")
	}
	return nil
}

func validateShowArray(key string, values []any) error {
	for _, value := range values {
		if key == "capabilities" {
			if _, ok := value.(string); !ok {
				return errors.New("model capabilities must contain strings")
			}
			continue
		}
		entry, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("model Show %s entries must be objects", key)
		}
		if key == "messages" {
			if err := validateShowMessage(entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runtime) readMetadata(ctx context.Context, method, path string, body []byte, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, r.url+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("owned runtime metadata returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("owned runtime metadata exceeds its byte limit")
	}
	return data, nil
}

func validateShowField(key string, value any) error {
	switch key {
	case "remote_model", "remote_host":
		text, ok := value.(string)
		if !ok || text != "" {
			return errors.New("owned model Show declares remote or malformed provenance")
		}
	case "license", "modified_at", "name", "digest", "modelfile", "template", "system", "renderer", "parser", "parameters", "requires":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("model Show %s must be a string", key)
		}
		if key == "parameters" && draftParameter.MatchString(text) {
			return errors.New("owned runtime profile does not support draft or MTP parameters")
		}
		if key == "modelfile" && extraComponent.MatchString(text) {
			return errors.New("owned text runtime cannot independently bind additional adapter, projector or draft bytes")
		}
	case "model_info", "projector_info", "details":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("model Show %s must be an object", key)
		}
		if key == "projector_info" && len(object) > 0 {
			return errors.New("owned text runtime does not support projector dependencies")
		}
	case "messages", "tensors", "capabilities":
		values, ok := value.([]any)
		if !ok || len(values) > 4096 {
			return fmt.Errorf("model Show %s must be a bounded array", key)
		}
		if err := validateShowArray(key, values); err != nil {
			return err
		}
	case "size":
		number, ok := value.(json.Number)
		if !ok {
			return errors.New("model Show size must be an integer")
		}
		n, err := number.Int64()
		if err != nil || n < 0 {
			return errors.New("model Show size must be nonnegative")
		}
	default:
		return fmt.Errorf("unsupported model Show field %q requires explicit review", key)
	}
	return nil
}

func validateShowMessage(entry map[string]any) error {
	if _, ok := entry["role"].(string); !ok {
		return errors.New("model message role must be a string")
	}
	if _, ok := entry["content"].(string); !ok {
		return errors.New("model message content must be a string")
	}
	if images, present := entry["images"]; present && images != nil {
		array, ok := images.([]any)
		if !ok || len(array) > 0 {
			return errors.New("owned text runtime does not support image message dependencies")
		}
	}

	return nil
}
