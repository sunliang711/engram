package auth

import "testing"

// TestScopedPermissionRoles ensures obsolete administrator roles cannot grant member access.
func TestScopedPermissionRoles(t *testing.T) {
	for _, tc := range []struct {
		role                                               string
		globalRead, globalWrite, projectRead, projectWrite bool
	}{
		{"none", false, false, false, false},
		{"member", true, false, false, false},
		{"global_writer", true, true, false, false},
		{"reader", false, false, true, false},
		{"writer", false, false, true, true},
		{"admin", false, false, false, false},
		{"maintainer", false, false, false, false},
		{"", false, false, false, false},
	} {
		t.Run(tc.role, func(t *testing.T) {
			if CanReadGlobal(tc.role) != tc.globalRead || CanWriteGlobal(tc.role) != tc.globalWrite || CanReadProject(tc.role) != tc.projectRead || CanWriteProject(tc.role) != tc.projectWrite {
				t.Fatalf("unexpected permissions for %q", tc.role)
			}
		})
	}
}
