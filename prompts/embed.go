// Package prompts provides the runtime prompt sets shipped with pintellect.
package prompts

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const OverrideEnv = "PINTELLECT_PROMPT_DIR"

// Embedded contains the runtime prompt sets. Upstream provenance is deliberately
// excluded: it is repository documentation rather than binary runtime content.
//
//go:embed commander workers skills
var Embedded embed.FS

// Set returns a filesystem and root for a runtime prompt set. During
// development, PINTELLECT_PROMPT_DIR may point to a checkout's prompts
// directory; otherwise it reads the prompt set compiled into the binary.
func Set(name string) (fs.FS, string, error) {
	if !isRuntimeSet(name) {
		return nil, "", fmt.Errorf("unknown runtime prompt set %q", name)
	}
	if root := strings.TrimSpace(os.Getenv(OverrideEnv)); root != "" {
		dir := filepath.Join(root, name)
		info, err := os.Stat(dir)
		if err != nil {
			return nil, "", fmt.Errorf("inspect %s override %s: %w", name, dir, err)
		}
		if !info.IsDir() {
			return nil, "", fmt.Errorf("%s override %s is not a directory", name, dir)
		}
		return os.DirFS(root), name, nil
	}
	return Embedded, name, nil
}

func isRuntimeSet(name string) bool {
	return name == "commander" || name == "workers" || name == "skills"
}
