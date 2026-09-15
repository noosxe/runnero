package limits

import "testing"

func TestParseLimits(t *testing.T) {
	// 1. CPU Limits
	nano, err := ParseCPULimit("2.5")
	if err != nil || nano != 2500000000 {
		t.Errorf("expected 2.5e9 nano cpus, got %d (err=%v)", nano, err)
	}
	nano, err = ParseCPULimit("")
	if err != nil || nano != 0 {
		t.Errorf("expected 0 for empty cpu, got %d", nano)
	}
	_, err = ParseCPULimit("-1")
	if err == nil {
		t.Errorf("expected error for negative cpu limit")
	}
	_, err = ParseCPULimit("invalid")
	if err == nil {
		t.Errorf("expected error for invalid cpu limit")
	}

	// 2. Memory Limits
	memTests := []struct {
		input    string
		expected int64
	}{
		{"4g", 4 * 1024 * 1024 * 1024},
		{"512m", 512 * 1024 * 1024},
		{"1024k", 1024 * 1024},
		{"1000b", 1000},
		{"", 0},
	}
	for _, tc := range memTests {
		bytes, err := ParseMemoryLimit(tc.input)
		if err != nil {
			t.Errorf("ParseMemoryLimit(%q) failed: %v", tc.input, err)
		}
		if bytes != tc.expected {
			t.Errorf("ParseMemoryLimit(%q) = %d, expected %d", tc.input, bytes, tc.expected)
		}
	}

	_, err = ParseMemoryLimit("bad-mem")
	if err == nil {
		t.Errorf("expected error for bad memory limit")
	}
	_, err = ParseMemoryLimit("-100m")
	if err == nil {
		t.Errorf("expected error for negative memory limit")
	}
}
