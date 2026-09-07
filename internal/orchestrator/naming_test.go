package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/noosxe/runnero/internal/orchestrator"
)

func TestSlugifyPoolName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "arm64-runners",
			expected: "arm64-runners",
		},
		{
			name:     "special characters and uppercase",
			input:    "My Pool @ Work #1",
			expected: "my-pool-work-1",
		},
		{
			name:     "leading and trailing hyphens",
			input:    "---pool-test---",
			expected: "pool-test",
		},
		{
			name:     "empty input",
			input:    "",
			expected: "pool",
		},
		{
			name:     "only symbols",
			input:    "$$$%%%^^^",
			expected: "pool",
		},
		{
			name:     "very long name exceeding 49 characters",
			input:    "this-is-an-extremely-long-runner-pool-name-that-definitely-exceeds-forty-nine-characters",
			expected: "this-is-an-extremely-long-runner-pool-name-that-d",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := orchestrator.SlugifyPoolName(tc.input)
			if got != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, got)
			}
			if len(got) > 49 {
				t.Errorf("slug length %d exceeds 49: %q", len(got), got)
			}
		})
	}
}

func TestGenerateContainerName(t *testing.T) {
	poolNames := []string{
		"default",
		"linux-arm64",
		"Production High CPU Runner Pool #99 with lots of text to test 64 chars",
	}

	for _, pool := range poolNames {
		name := orchestrator.GenerateContainerName(pool)
		if !strings.HasPrefix(name, "runnero-") {
			t.Errorf("expected prefix runnero-, got %q", name)
		}
		if len(name) > orchestrator.DockerMaxContainerNameLen {
			t.Errorf("container name %q exceeds max %d chars (length=%d)", name, orchestrator.DockerMaxContainerNameLen, len(name))
		}
	}
}
