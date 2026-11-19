package mcp

// projectRegisterDefinition describes explicit repository registration independently of memory writes.
func projectRegisterDefinition() map[string]any {
	return map[string]any{
		"name":        "project_register",
		"description": "Register an unregistered repository in the authenticated tenant. Requires the member's independent can_create_project permission; grants the creator writer access only to the new project. On PROJECT_NOT_REGISTERED call this tool, then memory_discover again before writing memories. Never use it to obtain access to an existing project.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Project name, usually the repository name; at most 200 characters."},
				"remotes": map[string]any{
					"type": "array", "maxItems": 32,
					"description": "Current repository Git remotes. Supply these or project_marker.",
					"items": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"name": map[string]any{"type": "string"},
							"url":  map[string]any{"type": "string"},
						},
						"required": []string{"name", "url"},
					},
				},
				"workspace_path":  map[string]any{"type": "string", "description": "Optional persistent monorepo directory for Git remote mappings, relative to the Git root. Requires remotes when nonempty; omit for marker-only registration. Never an absolute local path."},
				"project_marker":  map[string]any{"type": "string", "description": "Existing .engram-project UUID, if any; otherwise a marker is generated. Marker identity is independent of its directory. For marker-only registration omit workspace_path; report the marker's directory as marker_path in memory_discover."},
				"idempotency_key": map[string]any{"type": "string", "description": "Unique request key, at most 128 bytes. Reuse with the same arguments when retrying."},
			},
			"required": []string{"name", "idempotency_key"},
		},
	}
}
