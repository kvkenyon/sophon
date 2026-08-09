package workspace

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type Publication struct {
	ProjectKey string `json:"project_key"`
	Repository string `json:"repository"`
	RemoteName string `json:"remote_name"`
	RemoteURL  string `json:"remote_url"`
	Visibility string `json:"visibility"`
}

// PublishGitHub is an explicit operator-authorized resource creation action.
// Local project creation and development never call it. The operator supplies
// the exact repository and remote URL; Sophon does not invent an owner, URL,
// credential, or remote.
func (i Inspector) PublishGitHub(ctx context.Context, root, key, repository, remoteURL, visibility, ghBinary string, confirmed bool) (Publication, error) {
	if !confirmed {
		return Publication{}, errors.New("GitHub repository and remote creation requires explicit confirmation (--confirmed)")
	}
	if !repositoryPattern.MatchString(repository) || strings.TrimSpace(remoteURL) == "" {
		return Publication{}, errors.New("exact owner/repository and remote URL are required")
	}
	switch visibility {
	case "private", "public", "internal":
	default:
		return Publication{}, errors.New("visibility must be private, public, or internal")
	}
	project, err := i.Resolve(ctx, root, key)
	if err != nil {
		return Publication{}, err
	}
	if project.Remote != "" {
		return Publication{}, errors.New("project already has an origin remote; refusing replacement")
	}
	if strings.TrimSpace(ghBinary) == "" {
		ghBinary = "gh-axi"
	}
	args := []string{"repo", "create", repository, "--" + visibility}
	command := exec.CommandContext(ctx, ghBinary, args...)
	command.Dir = project.Path
	if output, err := command.CombinedOutput(); err != nil {
		return Publication{}, fmt.Errorf("gh-axi repository creation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	remote := exec.CommandContext(ctx, i.git(), "-C", project.Path, "remote", "add", "origin", remoteURL)
	if output, err := remote.CombinedOutput(); err != nil {
		return Publication{}, fmt.Errorf("repository was created but adding the exact origin remote failed; reconcile explicitly: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return Publication{ProjectKey: key, Repository: repository, RemoteName: "origin", RemoteURL: remoteURL, Visibility: visibility}, nil
}
