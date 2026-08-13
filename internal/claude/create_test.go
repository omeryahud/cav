package claude

import (
	"strings"
	"testing"
)

func TestCreateArgs(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		sname, prompt, model, effort string
		want                         string
	}{
		{"all set", "s", "do x", "fable", "max", "--bg --model fable --effort max --name s do x"},
		{"model+effort only", "", "", "fable", "max", "--bg --model fable --effort max"},
		{"no model/effort", "s", "", "", "", "--bg --name s"},
		{"model only", "", "", "opus", "", "--bg --model opus"},
		{"effort only", "", "", "", "high", "--bg --effort high"},
	} {
		got := strings.Join(createArgs(tc.sname, tc.prompt, tc.model, tc.effort), " ")
		if got != tc.want {
			t.Errorf("%s: createArgs = %q, want %q", tc.name, got, tc.want)
		}
	}
}
