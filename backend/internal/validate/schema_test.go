package validate

import (
	"encoding/json"
	"testing"
)

func TestSchemaValidate(t *testing.T) {
	schema := []byte(`{
		"type":"object",
		"additionalProperties":false,
		"required":["mount"],
		"properties":{
			"mount":{"type":"string","enum":["/","/data"]},
			"level":{"type":"integer","minimum":1,"maximum":10},
			"verbose":{"type":"boolean"}
		}
	}`)
	s, err := Parse(schema)
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]any{"mount": "/", "level": 3, "verbose": true}
	if err := s.Validate(good); err != nil {
		t.Fatalf("good payload rejected: %v", err)
	}
	if err := s.Validate(map[string]any{"mount": "/opt"}); err == nil {
		t.Fatal("enum violation accepted")
	}
	if err := s.Validate(map[string]any{"level": 1}); err == nil {
		t.Fatal("missing required accepted")
	}
	if err := s.Validate(map[string]any{"mount": "/", "extra": 1}); err == nil {
		t.Fatal("additional property accepted")
	}
	if err := s.Validate(map[string]any{"mount": "/", "level": 99}); err == nil {
		t.Fatal("maximum violation accepted")
	}
	// raw JSON roundtrip
	var raw any
	_ = json.Unmarshal([]byte(`{"mount":"/data"}`), &raw)
	if err := s.Validate(raw); err != nil {
		t.Fatalf("raw payload rejected: %v", err)
	}
}
