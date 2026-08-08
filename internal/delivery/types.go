// Package delivery provides the command-level Git and forge boundaries used
// by the delivery flow.
package delivery

import "errors"

var (
	ErrHeadMismatch   = errors.New("delivery head does not match the verified attempt head")
	ErrBranchMismatch = errors.New("delivery branch does not match the task attempt branch")
)

type PullRequest struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	HeadSHA    string `json:"head_sha"`
	URL        string `json:"url"`
	Number     int    `json:"number"`
}

type PullRequestInput struct {
	Repository string
	Worktree   string
	Branch     string
	HeadSHA    string
	Base       string
	Title      string
	Body       string
}
