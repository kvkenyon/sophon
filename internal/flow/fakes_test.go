package flow

import (
	"context"
	"errors"
	"sync"
	"time"

	"sophon/internal/delivery"
	gitcontrol "sophon/internal/git"
	"sophon/internal/herdr"
	"sophon/internal/readcode"
	"sophon/internal/treehouse"
	"sophon/internal/validation"
)

type fakeReviewProduct struct {
	mu          sync.Mutex
	open        readcode.OpenResult
	openErr     error
	status      readcode.StatusResult
	statusErr   error
	polls       []readcode.PollResult
	pollErr     error
	end         readcode.EndResult
	endErr      error
	openRequest readcode.OpenRequest
	statusCalls int
	pollAfter   []int
}

func (p *fakeReviewProduct) Open(_ context.Context, request readcode.OpenRequest) (readcode.OpenResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.openRequest = request
	return p.open, p.openErr
}

func (p *fakeReviewProduct) Status(_ context.Context, _ string) (readcode.StatusResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statusCalls++
	return p.status, p.statusErr
}

func (p *fakeReviewProduct) Poll(_ context.Context, _ string, after int, _ time.Duration) (readcode.PollResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pollAfter = append(p.pollAfter, after)
	if p.pollErr != nil {
		return readcode.PollResult{}, p.pollErr
	}
	if len(p.polls) == 0 {
		return readcode.PollResult{SchemaVersion: 1, SessionID: p.open.SessionID, After: after, NextCursor: after, TimedOut: true}, nil
	}
	result := p.polls[0]
	p.polls = p.polls[1:]
	return result, nil
}

func (p *fakeReviewProduct) End(_ context.Context, _ string) (readcode.EndResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.end, p.endErr
}

// fakeGit serves scripted Git answers keyed by worktree path.
type fakeGit struct {
	mu         sync.Mutex
	baseSHA    string
	headSHA    string
	branch     string
	createErr  error
	verifyErr  error
	snapshot   gitcontrol.Snapshot
	snapshotFn func(worktree string) gitcontrol.Snapshot
}

func (g *fakeGit) CreateTaskBranch(_ context.Context, worktree, branch string) (gitcontrol.Snapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.createErr != nil {
		return gitcontrol.Snapshot{}, g.createErr
	}
	g.branch = branch
	return gitcontrol.Snapshot{Head: g.baseSHA, Branch: branch, Clean: true}, nil
}

func (g *fakeGit) CreateTaskBranchAt(_ context.Context, worktree, branch, publicBranch, baseSHA string) (gitcontrol.Snapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.createErr != nil {
		return gitcontrol.Snapshot{}, g.createErr
	}
	g.branch = branch
	g.baseSHA = baseSHA
	return gitcontrol.Snapshot{Head: baseSHA, Branch: branch, Clean: true}, nil
}

func (g *fakeGit) Snapshot(_ context.Context, worktree string) (gitcontrol.Snapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.snapshotFn != nil {
		return g.snapshotFn(worktree), nil
	}
	if g.snapshot.Head != "" {
		return g.snapshot, nil
	}
	return gitcontrol.Snapshot{Head: g.headSHA, Branch: g.branch, Clean: true}, nil
}

func (g *fakeGit) VerifyCompletion(_ context.Context, worktree, baseSHA string) (gitcontrol.Completion, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.verifyErr != nil {
		return gitcontrol.Completion{}, g.verifyErr
	}
	return gitcontrol.Completion{BaseSHA: baseSHA, HeadSHA: g.headSHA, Branch: g.branch}, nil
}

// fakeLeases implements treehouse.CLI with scripted status and call capture.
type fakeLeases struct {
	mu        sync.Mutex
	alloc     treehouse.Allocation
	acquires  []string
	releases  []treehouse.Allocation
	statuses  []treehouse.WorktreeStatus
	statusErr error
}

func (l *fakeLeases) Acquire(_ context.Context, projectPath, holder string) (treehouse.Allocation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquires = append(l.acquires, holder)
	allocation := l.alloc
	allocation.LeaseHolder = holder
	return allocation, nil
}

func (l *fakeLeases) Release(_ context.Context, projectPath string, lease treehouse.Allocation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releases = append(l.releases, lease)
	return nil
}

func (l *fakeLeases) Status(_ context.Context, projectPath string) ([]treehouse.WorktreeStatus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.statusErr != nil {
		return nil, l.statusErr
	}
	return append([]treehouse.WorktreeStatus(nil), l.statuses...), nil
}

func (l *fakeLeases) released(i int) treehouse.Allocation {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.releases[i]
}

// fakePanes implements herdr.Adapter.
type fakePanes struct {
	mu         sync.Mutex
	session    herdr.Session
	startErr   error
	starts     []herdr.StartRequest
	observe    herdr.State
	observeErr error
	wakes      []string
}

func (p *fakePanes) StartCodex(_ context.Context, in herdr.StartRequest) (herdr.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts = append(p.starts, in)
	if p.startErr != nil {
		return herdr.Session{}, p.startErr
	}
	session := p.session
	session.Runtime = herdr.RuntimeCodex
	session.WorktreePath = in.WorktreePath
	if session.PaneID == "" {
		session.PaneID = "pane-1"
	}
	return session, nil
}

func (p *fakePanes) lastStart() herdr.StartRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.starts[len(p.starts)-1]
}

func (p *fakePanes) Observe(_ context.Context, session herdr.Session) (herdr.State, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.observeErr != nil {
		return "", p.observeErr
	}
	if p.observe == "" {
		return herdr.StateRunning, nil
	}
	return p.observe, nil
}

func (p *fakePanes) Wake(_ context.Context, session herdr.Session, message string) (herdr.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wakes = append(p.wakes, message)
	return session, nil
}

func (p *fakePanes) Submit(_ context.Context, session herdr.Session, message string) (herdr.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wakes = append(p.wakes, message)
	return session, nil
}

// fakeDeliveryGit implements DeliveryGit.
type fakeDeliveryGit struct {
	repository    string
	verifyErr     error
	repositoryE   error
	messages      []string
	messagesErr   error
	fetchErr      error
	descendantErr error
}

func (g *fakeDeliveryGit) CommitMessages(context.Context, string, string, string) ([]string, error) {
	if g.messagesErr != nil {
		return nil, g.messagesErr
	}
	if g.messages == nil {
		return []string{"Add the feature"}, nil
	}
	return g.messages, nil
}

func (g *fakeDeliveryGit) VerifyHead(context.Context, string, string, string) error {
	return g.verifyErr
}

func (g *fakeDeliveryGit) FetchBranch(context.Context, string, string, string) error {
	return g.fetchErr
}

func (g *fakeDeliveryGit) VerifyStrictDescendant(context.Context, string, string, string) error {
	return g.descendantErr
}

func (g *fakeDeliveryGit) Repository(context.Context, string) (string, error) {
	if g.repositoryE != nil {
		return "", g.repositoryE
	}
	return g.repository, nil
}

// fakeDeliveryRemote implements DeliveryRemote with a scriptable PR flow.
type fakeDeliveryRemote struct {
	mu                  sync.Mutex
	pushErr             error
	fastForwardWriteErr error
	headSHA             string
	headErr             error
	pr                  *delivery.PullRequest
	create              delivery.PullRequest
	createErr           error
	input               delivery.PullRequestInput
	branch              string
	branchExists        bool
	pushes              int
	creates             int
	finds               int
	fastForwards        int
	observed            *delivery.PullRequest
	defaultBranch       string
}

func (r *fakeDeliveryRemote) Push(_ context.Context, _, _, branch, headSHA string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes++
	if r.pushErr == nil {
		r.branch = branch
		r.headSHA = headSHA
		r.branchExists = true
	}
	return r.pushErr
}

func (r *fakeDeliveryRemote) PushFastForward(_ context.Context, _, _, branch, baseSHA, headSHA string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fastForwards++
	if r.pushErr == nil {
		r.branch = branch
		r.headSHA = headSHA
		r.branchExists = true
		if r.observed != nil {
			r.observed.HeadSHA = headSHA
		}
		if r.pr != nil {
			r.pr.HeadSHA = headSHA
		}
	}
	if r.fastForwardWriteErr != nil {
		return r.fastForwardWriteErr
	}
	return r.pushErr
}

func (r *fakeDeliveryRemote) FindPullRequest(context.Context, string, string, string, string) (*delivery.PullRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finds++
	if r.pr == nil {
		return nil, nil
	}
	copy := r.normalized(*r.pr)
	return &copy, nil
}

func (r *fakeDeliveryRemote) CreatePullRequest(_ context.Context, in delivery.PullRequestInput) (delivery.PullRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creates++
	r.input = in
	if r.createErr != nil {
		return delivery.PullRequest{}, r.createErr
	}
	created := r.normalized(r.create)
	r.observed = &created
	return created, nil
}

func (r *fakeDeliveryRemote) ObservePullRequest(context.Context, string, int) (delivery.PullRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.observed != nil {
		return r.normalized(*r.observed), nil
	}
	if r.pr != nil {
		return r.normalized(*r.pr), nil
	}
	return r.normalized(r.create), nil
}

func (r *fakeDeliveryRemote) DefaultBranch(context.Context, string, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.defaultBranch == "" {
		return "main", nil
	}
	return r.defaultBranch, nil
}

func (r *fakeDeliveryRemote) normalized(pr delivery.PullRequest) delivery.PullRequest {
	if pr.Repository == "" {
		pr.Repository = testRepo
	}
	if pr.Branch == "" {
		pr.Branch = r.branch
	}
	if pr.HeadSHA == "" {
		pr.HeadSHA = r.headSHA
	}
	if pr.BaseRepository == "" {
		pr.BaseRepository = testRepo
	}
	if pr.BaseBranch == "" {
		pr.BaseBranch = "main"
	}
	if pr.State == "" {
		pr.State = delivery.PullRequestOpen
	}
	return pr
}

func (r *fakeDeliveryRemote) BranchHead(context.Context, string, string, string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.headErr != nil {
		return "", false, r.headErr
	}
	return r.headSHA, r.branchExists, nil
}

func (r *fakeDeliveryRemote) HeadSHA(context.Context, string, string, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.headErr != nil {
		return "", r.headErr
	}
	return r.headSHA, nil
}

// fakeValidator returns a scripted validation result.
type fakeValidator struct {
	result validation.Result
	err    error
	runs   int
}

func (v *fakeValidator) Run(context.Context, string) (validation.Result, error) {
	v.runs++
	return v.result, v.err
}

// fakeSessionPanes implements SessionPanes with scripted answers and capture.
type fakeSessionPanes struct {
	mu          sync.Mutex
	runtime     herdr.Runtime
	state       herdr.State
	identifyErr error
	observeErr  error
	submitErr   error
	stopErr     error
	submissions []string
	stops       []herdr.Session
}

func (p *fakeSessionPanes) Identify(context.Context, herdr.Session) (herdr.Runtime, herdr.State, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.identifyErr != nil {
		return "", "", p.identifyErr
	}
	state := p.state
	if state == "" {
		state = herdr.StateIdle
	}
	return p.runtime, state, nil
}

func (p *fakeSessionPanes) Observe(_ context.Context, session herdr.Session) (herdr.State, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.observeErr != nil {
		return "", p.observeErr
	}
	if p.state == "" {
		return herdr.StateIdle, nil
	}
	return p.state, nil
}

func (p *fakeSessionPanes) Submit(_ context.Context, session herdr.Session, message string) (herdr.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.submitErr != nil {
		return herdr.Session{}, p.submitErr
	}
	p.submissions = append(p.submissions, session.PaneID+": "+message)
	return session, nil
}

func (p *fakeSessionPanes) Stop(_ context.Context, session herdr.Session) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopErr != nil {
		return p.stopErr
	}
	p.stops = append(p.stops, session)
	return nil
}

// sessionPanesFactory records every requested session and serves the fake.
func sessionPanesFactory(p *fakeSessionPanes, requested *[]string) func(string) SessionPanes {
	return func(session string) SessionPanes {
		*requested = append(*requested, session)
		return p
	}
}

var (
	errFake = errors.New("fake failure")
)
