package server

import (
	"encoding/json"
	"testing"
)

func TestEnsureChatInstruction(t *testing.T) {
	for _, tt := range []struct {
		name, input, role string
		count             int
	}{
		{"no system", `{"messages":[{"role":"user","content":"hi"}]}`, "system", 2},
		{"preserve system", `{"messages":[{"role":"system","content":"custom"},{"role":"user","content":"hi"}]}`, "system", 2},
		{"preserve developer", `{"messages":[{"role":"developer","content":"custom"},{"role":"user","content":"hi"}]}`, "developer", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ensureChatInstruction([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Messages []map[string]any `json:"messages"`
			}
			if err = json.Unmarshal(out, &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Messages) != tt.count || payload.Messages[0]["role"] != tt.role {
				t.Fatalf("unexpected messages: %v", payload.Messages)
			}
			if tt.name != "no system" && payload.Messages[0]["content"] != "custom" {
				t.Fatal("changed supplied instruction")
			}
		})
	}
}
