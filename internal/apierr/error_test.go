package apierr

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestInternalPreservesCauseWithoutSerializingIt(t *testing.T) {
	cause := errors.New("sql detail should stay server-side")
	err := Internal(cause)
	if err.Cause != cause {
		t.Fatalf("Cause=%v want %v", err.Cause, cause)
	}

	data, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatalf("Marshal: %v", marshalErr)
	}
	if strings.Contains(string(data), "sql detail") {
		t.Fatalf("serialized internal cause: %s", data)
	}
	if !strings.Contains(string(data), CodeInternal) {
		t.Fatalf("serialized error missing code: %s", data)
	}
}
