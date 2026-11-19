package memory

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"engram/internal/apperror"
)

const maxAgentNameChars = 128

// normalizeAgentName validates self-reported attribution without granting any authority to it.
func normalizeAgentName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxAgentNameChars {
		return "", apperror.New(apperror.CodeInvalidArgument, "agent_name must be valid UTF-8 and at most 128 characters")
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return "", apperror.New(apperror.CodeInvalidArgument, "agent_name must not contain control characters")
		}
	}
	return name, nil
}

// computeAgentRequestHash keeps legacy omitted-name retries compatible and binds named writes to their agent.
func computeAgentRequestHash(tool, agentName string, payload map[string]any) string {
	if agentName != "" {
		payload["agent_name"] = agentName
	}
	return computeRequestHash(tool, payload)
}
