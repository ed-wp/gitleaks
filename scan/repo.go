package scan

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/ed-wp/gitleaks/v7/config"
	"github.com/ed-wp/gitleaks/v7/options"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	log "github.com/sirupsen/logrus"
)

// RepoScanner is a repo scanner
type RepoScanner struct {
	opts     options.Options
	cfg      config.Config
	repo     *git.Repository
	throttle *Throttle
	repoName string
}

// NewRepoScanner returns a new repo scanner (go figure). This function also
// sets up the leak listener for multi-threaded awesomeness.
func NewRepoScanner(opts options.Options, cfg config.Config, repo *git.Repository) *RepoScanner {
	rs := &RepoScanner{
		opts:     opts,
		cfg:      cfg,
		repo:     repo,
		throttle: NewThrottle(opts),
		repoName: getRepoName(opts),
	}

	return rs
}

// Scan kicks of a repo scan
func (rs *RepoScanner) Scan() (Report, error) {
	var (
		scannerReport Report
		commits       chan *object.Commit
	)
	logOpts, err := logOptions(rs.repo, rs.opts)
	if err != nil {
		return scannerReport, err
	}
	cIter, err := rs.repo.Log(logOpts)
	if err != nil {
		return scannerReport, err
	}

	g, _ := errgroup.WithContext(context.Background())
	commits = make(chan *object.Commit)
	leaks := make(chan Leak)

	commitNum := 0
	g.Go(func() error {
		defer close(commits)
		err = cIter.ForEach(func(c *object.Commit) error {
			if c == nil || depthReached(commitNum, rs.opts) {
				return storer.ErrStop
			}

			if rs.cfg.Allowlist.CommitAllowed(c.Hash.String()) {
				return nil
			}
			commitNum++
			commits <- c
			if c.Hash.String() == rs.opts.CommitTo {
				return storer.ErrStop
			}

			return err
		})
		cIter.Close()
		return nil
	})

	for commit := range commits {
		c := commit
		rs.throttle.Limit()
		g.Go(func() error {
			commitScanner := NewCommitScanner(rs.opts, rs.cfg, rs.repo, c)
			commitScanner.SetRepoName(rs.repoName)
			report, err := commitScanner.Scan()
			rs.throttle.Release()
			if err != nil {
				log.Error(err)
			}
			for _, leak := range report.Leaks {
				leaks <- leak
			}
			return nil
		})
	}

	go func() {
		if err := g.Wait(); err != nil {
			log.Error(err)
		}
		close(leaks)
	}()

	for leak := range leaks {
		scannerReport.Leaks = append(scannerReport.Leaks, leak)
	}

	scannerReport.Commits = commitNum
	return scannerReport, g.Wait()
}

// SetRepoName sets the repo name
func (rs *RepoScanner) SetRepoName(repoName string) {
	rs.repoName = repoName
}
