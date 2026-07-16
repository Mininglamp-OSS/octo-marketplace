package skill

import "testing"

func TestParseLimit(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 20},
		{"0", 20},
		{"-1", 20},
		{"abc", 20},
		{"10", 10},
		{"50", 50},
		{"51", 50},
		{"100", 50},
		{"1", 1},
		{"20", 20},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseLimit(tt.input)
			if got != tt.expected {
				t.Errorf("parseLimit(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}
