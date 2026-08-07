package prompts

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedRuntimeSetsContainTheirContent(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	for _, test := range []struct {
		set, file, want string
	}{
		{"commander", "AGENTS.md", "# Parallel Intellect commander"},
		{"workers", "common.md", "# Common worker prompt"},
		{"skills", "status/SKILL.md", "# Status"},
	} {
		promptFS, root, err := Set(test.set)
		if err != nil {
			t.Fatal(err)
		}
		body, err := fs.ReadFile(promptFS, root+"/"+test.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), test.want) {
			t.Errorf("%s/%s omitted %q", test.set, test.file, test.want)
		}
	}
}
