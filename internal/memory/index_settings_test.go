package memory

import (
	"context"
	"testing"
)

func TestIndexSettings_ValidateBounds(t *testing.T) {
	valid := DefaultIndexSettings()
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected default settings to validate: %v", err)
	}

	minBounds := IndexSettings{
		DiscoverPinnedLimit:        MinDiscoverLimit,
		DiscoverGlobalRecentLimit:  MinDiscoverLimit,
		DiscoverProjectRecentLimit: MinDiscoverLimit,
		IndexSummaryMaxRunes:       MinIndexSummaryRunes,
	}
	if err := minBounds.Validate(); err != nil {
		t.Fatalf("expected min bounds to validate: %v", err)
	}

	maxBounds := IndexSettings{
		DiscoverPinnedLimit:        MaxDiscoverPinnedLimit,
		DiscoverGlobalRecentLimit:  MaxDiscoverGlobalLimit,
		DiscoverProjectRecentLimit: MaxDiscoverProjLimit,
		IndexSummaryMaxRunes:       MaxIndexSummaryRunes,
	}
	if err := maxBounds.Validate(); err != nil {
		t.Fatalf("expected max bounds to validate: %v", err)
	}

	invalidCases := []struct {
		name string
		mut  func(*IndexSettings)
	}{
		{"pinned below min", func(s *IndexSettings) { s.DiscoverPinnedLimit = 0 }},
		{"pinned above max", func(s *IndexSettings) { s.DiscoverPinnedLimit = MaxDiscoverPinnedLimit + 1 }},
		{"global below min", func(s *IndexSettings) { s.DiscoverGlobalRecentLimit = 0 }},
		{"global above max", func(s *IndexSettings) { s.DiscoverGlobalRecentLimit = MaxDiscoverGlobalLimit + 1 }},
		{"project below min", func(s *IndexSettings) { s.DiscoverProjectRecentLimit = 0 }},
		{"project above max", func(s *IndexSettings) { s.DiscoverProjectRecentLimit = MaxDiscoverProjLimit + 1 }},
		{"summary negative", func(s *IndexSettings) { s.IndexSummaryMaxRunes = -1 }},
		{"summary above max", func(s *IndexSettings) { s.IndexSummaryMaxRunes = MaxIndexSummaryRunes + 1 }},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultIndexSettings()
			tc.mut(&s)
			if err := s.Validate(); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestResolveScopeIndexLimits(t *testing.T) {
	custom := IndexSettings{
		DiscoverPinnedLimit:        25,
		DiscoverGlobalRecentLimit:  15,
		DiscoverProjectRecentLimit: 150,
		IndexSummaryMaxRunes:       0,
	}

	p, r, sum := resolveScopeIndexLimits("global", custom)
	if p != 25 || r != 15 || sum != 0 {
		t.Fatalf("unexpected global limits: pinned=%d recent=%d summary=%d", p, r, sum)
	}

	p, r, sum = resolveScopeIndexLimits("project", custom)
	if p != 25 || r != 150 || sum != 0 {
		t.Fatalf("unexpected project limits: pinned=%d recent=%d summary=%d", p, r, sum)
	}

	p, r, sum = resolveScopeIndexLimits("project", IndexSettings{IndexSummaryMaxRunes: -5})
	if p != DefaultDiscoverPinnedLimit || r != DefaultDiscoverProjectRecentLimit || sum != 0 {
		t.Fatalf("unexpected fallback limits: pinned=%d recent=%d summary=%d", p, r, sum)
	}
}

func TestService_GetIndexSettingsNilPoolReturnsDefaults(t *testing.T) {
	var svc *Service
	got, err := svc.GetIndexSettings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != DefaultIndexSettings() {
		t.Fatalf("expected defaults %+v, got %+v", DefaultIndexSettings(), got)
	}
}
