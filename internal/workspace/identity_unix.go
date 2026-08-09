//go:build unix

package workspace

import (
	"fmt"
	"os"
	"syscall"
)

func filesystemIdentity(projectPath, gitCommon string) (string, error) {
	projectInfo, err := os.Stat(projectPath)
	if err != nil {
		return "", err
	}
	gitInfo, err := os.Stat(gitCommon)
	if err != nil {
		return "", err
	}
	projectStat, ok := projectInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("cannot read project filesystem identity")
	}
	gitStat, ok := gitInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("cannot read Git filesystem identity")
	}
	return fmt.Sprintf("project:%d:%d;git:%d:%d", projectStat.Dev, projectStat.Ino, gitStat.Dev, gitStat.Ino), nil
}
