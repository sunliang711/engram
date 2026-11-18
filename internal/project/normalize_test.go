package project

import "testing"

func TestNormalizeRemote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "scp ssh",
			in:   "git@github.com:eagle/project-a.git",
			want: "github.com/eagle/project-a",
		},
		{
			name: "https with .git",
			in:   "https://github.com/eagle/project-a.git",
			want: "github.com/eagle/project-a",
		},
		{
			name: "ssh scheme with port and credentials",
			in:   "ssh://git@GITHUB.COM:22/eagle/project-a.git/",
			want: "github.com/eagle/project-a",
		},
		{
			name: "https with embedded credentials and extra slashes",
			in:   "https://user:secret_token@GitLab.Example.com:8443//team//SubRepo.git///",
			want: "gitlab.example.com/team/SubRepo",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRemote(tc.in)
			if err != nil {
				t.Fatalf("NormalizeRemote(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeRemote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeRelPath(t *testing.T) {
	valid := map[string]string{
		"":                  "",
		".":                 "",
		"services/api":      "services/api",
		"services//api///":  "services/api",
		"packages/core/v1/": "packages/core/v1",
	}
	for in, want := range valid {
		got, err := NormalizeRelPath(in)
		if err != nil {
			t.Fatalf("NormalizeRelPath(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Fatalf("NormalizeRelPath(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{
		"/abs/path",
		"../escape",
		"services/../../escape",
		"services/./api",
		"services\\api",
	}
	for _, in := range invalid {
		if _, err := NormalizeRelPath(in); err == nil {
			t.Fatalf("NormalizeRelPath(%q) expected error, got nil", in)
		}
	}
}
