package role

import (
	"testing"

	"github.com/blisspixel/fitr/internal/record"
)

func TestReadAttachedRecordRejectsAnEmptyAttachment(t *testing.T) {
	_, err := ReadAttachedRecord(Attachment{}, record.Store{})
	if err == nil {
		t.Fatal("empty attachment loaded a record")
	}
}
