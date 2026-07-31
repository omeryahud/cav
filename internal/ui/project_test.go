package ui

import (
	"path/filepath"
	"testing"
)

// N may only create directories inside the configured project root; a name
// containing "../" must not escape it.
func TestValidProjectPath(t *testing.T) {
	root := "/home/u/go/src/github.com/u"
	for _, tc := range []struct {
		name string
		cwd  string
		ok   bool
	}{
		{"the root itself", root, true},
		{"direct child", root + "/newproj", true},
		{"nested child", root + "/a/b", true},
		{"unclean but inside", root + "/./newproj", true},
		{"parent", filepath.Dir(root), false},
		{"sibling", filepath.Dir(root) + "/other", false},
		{"escape via ..", root + "/../evil", false},
		{"deep escape via ..", root + "/a/../../evil", false},
		{"prefix lookalike", root + "-evil", false},
		{"unrelated", "/tmp/evil", false},
		{"empty", "", false},
	} {
		err := validProjectPath(tc.cwd, root)
		if tc.ok && err != nil {
			t.Errorf("%s: validProjectPath(%q) = %v, want nil", tc.name, tc.cwd, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: validProjectPath(%q) = nil, want an error", tc.name, tc.cwd)
		}
	}
}

// A trailing slash on the configured root shouldn't change what's accepted.
func TestValidProjectPathNormalizesRoot(t *testing.T) {
	if err := validProjectPath("/root/proj", "/root/"); err != nil {
		t.Errorf("trailing slash on root should still accept a child: %v", err)
	}
}
