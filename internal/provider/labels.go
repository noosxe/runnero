package provider

import (
	"encoding/json"
	"strings"
)

// LabelsMatch checks whether a runner pool configured with poolLabelsRaw provides
// all the labels required by a workflow job (docs/24 §5.2). Shared by the
// webhook fast path (orchestrator delegates to this) and the demand-polling
// job counting, so both demand signals apply identical label semantics:
// matching is case-insensitive, whitespace-tolerant, and an empty required set
// always matches. An empty pool label set defaults to the runner image's
// built-in labels (self-hosted, linux).
func LabelsMatch(poolLabelsRaw string, requiredLabels []string) bool {
	if len(requiredLabels) == 0 {
		return true
	}

	poolLabelsList := ParsePoolLabels(poolLabelsRaw)
	poolLabelSet := make(map[string]struct{}, len(poolLabelsList))
	for _, l := range poolLabelsList {
		poolLabelSet[strings.ToLower(strings.TrimSpace(l))] = struct{}{}
	}

	for _, req := range requiredLabels {
		clean := strings.ToLower(strings.TrimSpace(req))
		if clean == "" {
			continue
		}
		if _, ok := poolLabelSet[clean]; !ok {
			return false
		}
	}
	return true
}

// ParsePoolLabels parses the pool's stored label contract: a JSON array when
// valid, otherwise a comma-separated list. Empty input falls back to the
// default runner labels.
func ParsePoolLabels(raw string) []string {
	if raw == "" {
		return []string{"self-hosted", "linux"}
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err == nil && len(arr) > 0 {
		return arr
	}
	parts := strings.Split(raw, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	if len(res) == 0 {
		return []string{"self-hosted", "linux"}
	}
	return res
}
