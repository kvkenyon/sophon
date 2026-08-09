//go:build !unix

package workspace

import "fmt"

func filesystemIdentity(projectPath, gitCommon string) (string, error) {
	return "", fmt.Errorf("workspace project identity is unsupported on this platform")
}
