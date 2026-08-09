// Package workspace owns Sophon's ordinary multi-project workspace boundary.
// A workspace is an immutable marker plus a direct projects directory. It is
// organization and commander scope only: task truth remains in internal/store.
package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"sophon/internal/id"
)

const (
	MarkerName  = ".sophon-workspace.json"
	ProjectsDir = "projects"
	Version     = 1
)

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Marker struct {
	Version     int       `json:"version"`
	ID          string    `json:"id"`
	Root        string    `json:"root"`
	ProjectsDir string    `json:"projects_dir"`
	CreatedAt   time.Time `json:"created_at"`
}

type Project struct {
	Key           string `json:"key"`
	Path          string `json:"path"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceRoot string `json:"workspace_root"`
	Identity      string `json:"identity"`
	GitCommon     string `json:"git_common_dir"`
	Remote        string `json:"remote,omitempty"`
	Head          string `json:"head,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Unborn        bool   `json:"unborn"`
}

type Inspector struct {
	GitBinary string
}

func (i Inspector) git() string {
	if strings.TrimSpace(i.GitBinary) == "" {
		return "git"
	}
	return i.GitBinary
}

func Init(root string) (Marker, error) {
	abs, err := cleanAbsolute(root)
	if err != nil {
		return Marker{}, err
	}
	if err := refuseNestedWorkspace(abs); err != nil {
		return Marker{}, err
	}
	info, err := os.Lstat(abs)
	createdRoot := false
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(abs, 0o700); err != nil {
			return Marker{}, fmt.Errorf("create workspace root: %w", err)
		}
		createdRoot = true
		info, err = os.Lstat(abs)
	}
	if err != nil {
		return Marker{}, fmt.Errorf("inspect workspace root: %w", err)
	}
	if err := safeDirectory(abs, info); err != nil {
		return Marker{}, err
	}
	markerPath := filepath.Join(abs, MarkerName)
	if _, err := os.Lstat(markerPath); err == nil {
		return Read(abs)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Marker{}, fmt.Errorf("inspect workspace marker: %w", err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return Marker{}, fmt.Errorf("inspect workspace contents: %w", err)
	}
	if len(entries) > 0 {
		// A crash after creating only the conventional projects directory is
		// the one safe partial initialization that may converge.
		if len(entries) != 1 || entries[0].Name() != ProjectsDir || !entries[0].IsDir() {
			return Marker{}, errors.New("workspace initialization refuses an existing unrelated root")
		}
	}
	projects := filepath.Join(abs, ProjectsDir)
	if err := os.Mkdir(projects, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return Marker{}, fmt.Errorf("create projects directory: %w", err)
	}
	projectsInfo, err := os.Lstat(projects)
	if err != nil {
		return Marker{}, err
	}
	if err := safeDirectory(projects, projectsInfo); err != nil {
		return Marker{}, err
	}
	workspaceID, err := id.New("workspace")
	if err != nil {
		return Marker{}, err
	}
	marker := Marker{Version: Version, ID: workspaceID, Root: abs, ProjectsDir: ProjectsDir, CreatedAt: time.Now().UTC()}
	if err := publishExclusive(markerPath, marker); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Read(abs)
		}
		if createdRoot {
			_ = os.Remove(projects)
			_ = os.Remove(abs)
		}
		return Marker{}, err
	}
	return marker, nil
}

func Read(root string) (Marker, error) {
	abs, err := cleanAbsolute(root)
	if err != nil {
		return Marker{}, err
	}
	if err := refuseNestedWorkspace(abs); err != nil {
		return Marker{}, err
	}
	rootInfo, err := os.Lstat(abs)
	if err != nil {
		return Marker{}, fmt.Errorf("inspect workspace root: %w", err)
	}
	if err := safeDirectory(abs, rootInfo); err != nil {
		return Marker{}, err
	}
	markerPath := filepath.Join(abs, MarkerName)
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		return Marker{}, fmt.Errorf("read workspace marker: %w", err)
	}
	if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
		return Marker{}, errors.New("workspace marker must be a regular file")
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return Marker{}, err
	}
	var marker Marker
	if err := decodeStrict(data, &marker); err != nil {
		return Marker{}, fmt.Errorf("decode workspace marker: %w", err)
	}
	if marker.Version != Version || !strings.HasPrefix(marker.ID, "workspace_") || marker.ProjectsDir != ProjectsDir || marker.CreatedAt.IsZero() {
		return Marker{}, errors.New("workspace marker has an unsupported or incomplete identity")
	}
	if marker.Root != abs {
		return Marker{}, fmt.Errorf("workspace root moved or conflicts with marker: recorded %s, observed %s", marker.Root, abs)
	}
	projects := filepath.Join(abs, ProjectsDir)
	projectsInfo, err := os.Lstat(projects)
	if err != nil {
		return Marker{}, fmt.Errorf("inspect projects directory: %w", err)
	}
	if err := safeDirectory(projects, projectsInfo); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

func (i Inspector) List(ctx context.Context, root string) ([]Project, error) {
	marker, err := Read(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(marker.Root, ProjectsDir))
	if err != nil {
		return nil, err
	}
	var projects []Project
	seenCommon := map[string]string{}
	seenFolded := map[string]string{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		folded := strings.ToLower(entry.Name())
		if prior := seenFolded[folded]; prior != "" && prior != entry.Name() {
			return nil, fmt.Errorf("project key case collision: %s and %s", prior, entry.Name())
		}
		seenFolded[folded] = entry.Name()
		project, err := i.Resolve(ctx, marker.Root, entry.Name())
		if err != nil {
			return nil, err
		}
		if prior := seenCommon[project.GitCommon]; prior != "" {
			return nil, fmt.Errorf("projects %s and %s share one Git identity", prior, project.Key)
		}
		seenCommon[project.GitCommon] = project.Key
		projects = append(projects, project)
	}
	sort.Slice(projects, func(a, b int) bool { return projects[a].Key < projects[b].Key })
	return projects, nil
}

func (i Inspector) Resolve(ctx context.Context, root, key string) (Project, error) {
	if err := ValidateKey(key); err != nil {
		return Project{}, err
	}
	marker, err := Read(root)
	if err != nil {
		return Project{}, err
	}
	projectsRoot := filepath.Join(marker.Root, ProjectsDir)
	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		return Project{}, err
	}
	for _, entry := range entries {
		if !strings.EqualFold(entry.Name(), key) {
			continue
		}
		if entry.Name() != key {
			return Project{}, fmt.Errorf("project key %q collides with existing %q", key, entry.Name())
		}
	}
	path := filepath.Join(projectsRoot, key)
	info, err := os.Lstat(path)
	if err != nil {
		return Project{}, fmt.Errorf("inspect project %q: %w", key, err)
	}
	if err := safeDirectory(path, info); err != nil {
		return Project{}, fmt.Errorf("project %q: %w", key, err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path || filepath.Dir(real) != projectsRoot {
		return Project{}, fmt.Errorf("project %q escapes the real projects directory", key)
	}
	top, err := i.output(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return Project{}, fmt.Errorf("project %q is not a valid Git repository: %w", key, err)
	}
	top, err = filepath.Abs(top)
	if err != nil || filepath.Clean(top) != path {
		return Project{}, fmt.Errorf("project %q Git top-level is not its confined child path", key)
	}
	common, err := i.output(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Project{}, fmt.Errorf("resolve project %q Git identity: %w", key, err)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project %q Git common directory: %w", key, err)
	}
	identity, err := filesystemIdentity(path, common)
	if err != nil {
		return Project{}, err
	}
	project := Project{Key: key, Path: path, WorkspaceID: marker.ID, WorkspaceRoot: marker.Root, Identity: identity, GitCommon: common}
	if head, headErr := i.output(ctx, path, "rev-parse", "HEAD"); headErr == nil {
		project.Head = head
	} else {
		project.Unborn = true
	}
	if branch, branchErr := i.output(ctx, path, "symbolic-ref", "--short", "HEAD"); branchErr == nil {
		project.Branch = branch
	}
	if remote, remoteErr := i.output(ctx, path, "remote", "get-url", "origin"); remoteErr == nil {
		project.Remote = remote
	}
	return project, nil
}

func (i Inspector) Create(ctx context.Context, root, key, initialBranch string) (Project, error) {
	if initialBranch == "" {
		initialBranch = "main"
	}
	if err := ValidateKey(key); err != nil {
		return Project{}, err
	}
	marker, err := Read(root)
	if err != nil {
		return Project{}, err
	}
	if err := i.refuseCollision(ctx, marker.Root, key); err != nil {
		return Project{}, err
	}
	target := filepath.Join(marker.Root, ProjectsDir, key)
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Project{}, fmt.Errorf("project %q already exists", key)
		}
		return Project{}, err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return Project{}, err
	}
	command := exec.CommandContext(ctx, i.git(), "init", "-b", initialBranch, target)
	if output, err := command.CombinedOutput(); err != nil {
		return Project{}, fmt.Errorf("initialize project Git repository: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return i.Resolve(ctx, marker.Root, key)
}

func (i Inspector) Clone(ctx context.Context, root, key, source string) (Project, error) {
	if strings.TrimSpace(source) == "" {
		return Project{}, errors.New("clone source is required")
	}
	if err := ValidateKey(key); err != nil {
		return Project{}, err
	}
	marker, err := Read(root)
	if err != nil {
		return Project{}, err
	}
	if err := i.refuseCollision(ctx, marker.Root, key); err != nil {
		return Project{}, err
	}
	target := filepath.Join(marker.Root, ProjectsDir, key)
	command := exec.CommandContext(ctx, i.git(), "clone", "--origin", "origin", "--", source, target)
	if output, err := command.CombinedOutput(); err != nil {
		return Project{}, fmt.Errorf("clone project: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return i.Resolve(ctx, marker.Root, key)
}

func (i Inspector) Add(ctx context.Context, root, key string) (Project, error) {
	project, err := i.Resolve(ctx, root, key)
	if err != nil {
		return Project{}, err
	}
	projects, err := i.List(ctx, root)
	if err != nil {
		return Project{}, err
	}
	for _, candidate := range projects {
		if candidate.Key != project.Key && candidate.GitCommon == project.GitCommon {
			return Project{}, fmt.Errorf("project %q duplicates Git identity of %q", key, candidate.Key)
		}
	}
	return project, nil
}

func ValidatePinned(observed Project, workspaceID, key, path, identity string) error {
	if observed.WorkspaceID != workspaceID || observed.Key != key || observed.Path != path || observed.Identity != identity {
		return errors.New("workspace project identity drifted; inspect or explicitly adopt the current project before new work")
	}
	return nil
}

func ValidateKey(key string) error {
	if !keyPattern.MatchString(key) || key == "projects" {
		return fmt.Errorf("project key %q must be a lowercase filesystem-safe name", key)
	}
	return nil
}

func (i Inspector) refuseCollision(ctx context.Context, root, key string) error {
	entries, err := os.ReadDir(filepath.Join(root, ProjectsDir))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), key) {
			return fmt.Errorf("project key %q collides with existing %q", key, entry.Name())
		}
	}
	_, err = i.List(ctx, root)
	return err
}

func (i Inspector) output(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, i.git(), append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func cleanAbsolute(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if _, err := os.Lstat(abs); err == nil {
		info, infoErr := os.Lstat(abs)
		if infoErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("workspace root must not be a symlink")
		}
		real, evalErr := filepath.EvalSymlinks(abs)
		if evalErr != nil {
			return "", evalErr
		}
		return filepath.Clean(real), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func safeDirectory(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s must be a real directory, not a symlink or file", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s has unsafe group/world-writable permissions", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(real) != filepath.Clean(path) {
		return fmt.Errorf("%s resolves through a symlink", path)
	}
	return nil
}

func refuseNestedWorkspace(root string) error {
	parent := filepath.Dir(root)
	for parent != filepath.Dir(parent) {
		if _, err := os.Lstat(filepath.Join(parent, MarkerName)); err == nil {
			return fmt.Errorf("workspace root %s is nested inside workspace %s", root, parent)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent = filepath.Dir(parent)
	}
	return nil
}

func publishExclusive(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("unexpected trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
