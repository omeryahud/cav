package main

import "testing"

func TestParseArgs(t *testing.T) {
	const cwd = "/work/dir"
	for _, tc := range []struct {
		name                     string
		args                     []string
		filter, open, dir, sname string
		attach                   bool
		wantErr                  bool
	}{
		{name: "bare", args: nil},
		{name: "filter term", args: []string{"api", "fix"}, filter: "api fix"},
		{name: "open", args: []string{"-o", "my", "sess"}, open: "my sess"},
		{name: "open long", args: []string{"--open", "x"}, open: "x"},
		{name: "open missing name", args: []string{"-o"}, wantErr: true},
		{name: "new bare", args: []string{"-n"}, dir: cwd},
		{name: "new named", args: []string{"-n", "api", "fix"}, dir: cwd, sname: "api fix"},
		{name: "new attach", args: []string{"-n", "-a"}, dir: cwd, attach: true},
		{name: "attach before new", args: []string{"-a", "--new", "x"}, dir: cwd, sname: "x", attach: true},
		{name: "attach alone", args: []string{"-a"}, wantErr: true},
		{name: "attach with open", args: []string{"-o", "x", "-a"}, wantErr: true},
		{name: "attach with filter", args: []string{"term", "-a"}, wantErr: true},
	} {
		opts, err := parseArgs(tc.args, cwd)
		if tc.wantErr {
			if err == nil || err.Error() == "" {
				t.Errorf("%s: want a usage error, got opts=%+v err=%v", tc.name, opts, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
			continue
		}
		if opts.Filter != tc.filter || opts.Open != tc.open ||
			opts.NewInDir != tc.dir || opts.NewName != tc.sname || opts.AttachNew != tc.attach {
			t.Errorf("%s: got %+v", tc.name, opts)
		}
	}
}

func TestParseArgsHelpIsCleanExit(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}} {
		_, err := parseArgs(args, "/x")
		if err == nil || err.Error() != "" {
			t.Errorf("%v: want the empty sentinel error (usage, exit 0), got %v", args, err)
		}
	}
}
