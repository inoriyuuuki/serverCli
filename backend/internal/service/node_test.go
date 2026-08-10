package service

import (
	"encoding/json"
	"testing"
)

func TestNormalizeLabels(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
		err  bool
	}{
		{name: "object", raw: `{"env":"prod","team":"ops"}`, want: map[string]string{"env": "prod", "team": "ops"}},
		{name: "empty object", raw: `{}`, want: map[string]string{}},
		{name: "array k=v", raw: `["env=prod","team=ops"]`, want: map[string]string{"env": "prod", "team": "ops"}},
		{name: "array bare", raw: `["managed"]`, want: map[string]string{"managed": "true"}},
		{name: "array empty", raw: `[]`, want: map[string]string{}},
		{name: "null", raw: `null`, want: nil},
		{name: "invalid", raw: `42`, err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeLabels(json.RawMessage(c.raw))
			if c.err {
				if err == nil {
					t.Fatalf("expected error for %s", c.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("length mismatch: got %v want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Fatalf("mismatch for %s: got %v want %v", k, got, c.want)
				}
			}
		})
	}
}
