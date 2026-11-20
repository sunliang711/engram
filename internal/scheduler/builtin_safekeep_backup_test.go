package scheduler

import "testing"

// TestSafekeepBackupConfigRejectsUploadOverrides prevents a second persisted upload configuration.
func TestSafekeepBackupConfigRejectsUploadOverrides(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     map[string]any
		invalid bool
	}{
		{"defaults", nil, false},
		{"export options", map[string]any{"migration_mode": "knowledge", "include_deleted": false}, false},
		{"empty legacy fields", map[string]any{"upload_mode": " ", "remote_path": "", "targets": nil}, false},
		{"upload mode", map[string]any{"upload_mode": "sync"}, true},
		{"remote path", map[string]any{"remote_path": "legacy/path"}, true},
		{"targets", map[string]any{"targets": "legacy-upstream"}, true},
		{"invalid override type", map[string]any{"remote_path": true}, true},
		{"invalid export mode", map[string]any{"migration_mode": "invalid"}, true},
		{"invalid deletion option", map[string]any{"include_deleted": "false"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := validateSafekeepBackupConfig(tc.raw)
			if (err != nil) != tc.invalid {
				t.Fatalf("config=%v err=%v", cfg, err)
			}
			if tc.invalid {
				return
			}
			if len(cfg) != 2 {
				t.Fatalf("unexpected persisted fields: %v", cfg)
			}
			if tc.name == "export options" && (cfg["migration_mode"] != "knowledge" || cfg["include_deleted"] != false) {
				t.Fatalf("export options changed: %v", cfg)
			}
		})
	}
}
