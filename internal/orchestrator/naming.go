package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// Standard metadata labels applied to all supervisor-managed containers (docs/03 §2).
	LabelManaged   = "com.runnero.managed"
	LabelPoolName  = "com.runnero.pool-name"
	LabelID        = "com.runnero.id"
	LabelSpawnedAt = "com.runnero.spawned-at"
	LabelTaskType  = "com.runnero.task-type"
	LabelTargetURL = "com.runnero.target-url"

	// Task types
	TaskTypeRunner = "runner"
	TaskTypeJob    = "task"

	// DefaultRunnerImage is the standard unified runner image.
	DefaultRunnerImage = "ghcr.io/noosxe/runnero:latest"

	// DockerMaxContainerNameLen is the maximum container name length enforced by Docker.
	DockerMaxContainerNameLen = 64
)

var nonAlphaNumRegex = regexp.MustCompile(`[^a-z0-9]+`)

// GenerateContainerName creates a container name according to OQ #23:
// runnero-<pool-slug>-<6-hex> with a total length <= 64 characters.
func GenerateContainerName(poolName string) string {
	slug := SlugifyPoolName(poolName)
	randomHex := randomHexSuffix(6)
	return fmt.Sprintf("runnero-%s-%s", slug, randomHex)
}

// SlugifyPoolName sanitizes and truncates a pool name to fit within the 64-char limit.
// Prefix "runnero-" is 8 chars, suffix "-xxxxxx" is 7 chars => max slug length is 49 chars.
func SlugifyPoolName(poolName string) string {
	lower := strings.ToLower(strings.TrimSpace(poolName))
	cleaned := nonAlphaNumRegex.ReplaceAllString(lower, "-")
	trimmed := strings.Trim(cleaned, "-")

	if trimmed == "" {
		trimmed = "pool"
	}

	maxSlugLen := DockerMaxContainerNameLen - len("runnero-") - len("-123456")
	if len(trimmed) > maxSlugLen {
		trimmed = strings.TrimRight(trimmed[:maxSlugLen], "-")
	}
	if trimmed == "" {
		trimmed = "pool"
	}
	return trimmed
}

func randomHexSuffix(length int) string {
	bytesNeeded := (length + 1) / 2
	b := make([]byte, bytesNeeded)
	if _, err := rand.Read(b); err != nil {
		// Fallback timestamp hex if crypto/rand fails
		return fmt.Sprintf("%06x", time.Now().UnixNano()%0xFFFFFF)[:length]
	}
	return hex.EncodeToString(b)[:length]
}
