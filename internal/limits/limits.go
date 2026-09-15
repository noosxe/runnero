package limits

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultPidsLimit is the shipped per-pool process ceiling (RUN-148):
// comfortable headroom for JVM/npm-heavy jobs, a hard wall for fork bombs.
const DefaultPidsLimit int64 = 4096

// ParseCPULimit converts CPU limits like "2.0", "0.5", "1" to NanoCPUs (int64).
// Lives beside RunnerConfig (whose field formats it defines) so both the API
// validation layer and the Docker engine can share it (RUN-147).
func ParseCPULimit(cpu string) (int64, error) {
	trimmed := strings.TrimSpace(cpu)
	if trimmed == "" {
		return 0, nil
	}
	val, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cpu limit %q: %w", cpu, err)
	}
	if val <= 0 {
		return 0, fmt.Errorf("cpu limit must be positive: %q", cpu)
	}
	return int64(val * 1e9), nil
}

// ParseMemoryLimit parses strings like "4g", "512m", "1024k" to byte count (int64).
func ParseMemoryLimit(mem string) (int64, error) {
	s := strings.TrimSpace(strings.ToLower(mem))
	if s == "" {
		return 0, nil
	}
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, "g") || strings.HasSuffix(s, "gb"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "b"), "g")
	case strings.HasSuffix(s, "m") || strings.HasSuffix(s, "mb"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "b"), "m")
	case strings.HasSuffix(s, "k") || strings.HasSuffix(s, "kb"):
		multiplier = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "b"), "k")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimSuffix(s, "b")
	}

	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory limit %q: %w", mem, err)
	}
	if val <= 0 {
		return 0, fmt.Errorf("memory limit must be positive: %q", mem)
	}
	return int64(val * float64(multiplier)), nil
}
