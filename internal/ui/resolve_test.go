package ui

import (
	"testing"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/labels"
	"github.com/omeryahud/cav/internal/names"
	"github.com/omeryahud/cav/internal/seen"
)

// resolveModel builds a Model with empty cav-local stores (XDG pointed at a
// temp dir) so displayName resolves purely from the session names given.
func resolveModel(t *testing.T, sessionNames ...string) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := &Model{names: names.Load(), labels: labels.Load(), seen: seen.Load()}
	for i, n := range sessionNames {
		m.all = append(m.all, claude.Session{
			SessionID: string(rune('a'+i)) + "-sid",
			Name:      n,
			Kind:      "background",
		})
	}
	return m
}

func namesOf(ss []claude.Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

func TestResolveByNameTiers(t *testing.T) {
	m := resolveModel(t, "api", "api-fix", "grande-api-fix", "unrelated")
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"api", []string{"api"}},                       // exact beats prefix ("api-fix") and substring
		{"API", []string{"api"}},                       // case-insensitive
		{"api-", []string{"api-fix"}},                  // unique prefix
		{"api-f", []string{"api-fix"}},                 // prefix tier excludes the substring-only match
		{"nde-api", []string{"grande-api-fix"}},        // unique substring
		{"fix", []string{"api-fix", "grande-api-fix"}}, // ambiguous: no prefix tier, two substrings
		{"zzz", nil}, // no match
	} {
		got := namesOf(m.resolveByName(tc.q))
		if len(got) != len(tc.want) {
			t.Errorf("resolveByName(%q) = %v, want %v", tc.q, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("resolveByName(%q) = %v, want %v", tc.q, got, tc.want)
				break
			}
		}
	}
}

func TestResolveByNamePrefixAmbiguity(t *testing.T) {
	m := resolveModel(t, "deploy-a", "deploy-b")
	if got := m.resolveByName("deploy"); len(got) != 2 {
		t.Errorf("ambiguous prefix should return both, got %v", namesOf(got))
	}
}

func TestResolveByNameUsesRenameOverride(t *testing.T) {
	m := resolveModel(t, "raw-name")
	if err := m.names.Set("a-sid", "renamed"); err != nil {
		t.Fatal(err)
	}
	if got := m.resolveByName("renamed"); len(got) != 1 {
		t.Fatalf("rename override should resolve, got %v", namesOf(got))
	}
	if got := m.resolveByName("raw-name"); len(got) != 0 {
		t.Errorf("the overridden raw name should no longer resolve, got %v", namesOf(got))
	}
}
