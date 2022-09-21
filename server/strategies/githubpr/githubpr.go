package githubpr

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/fluxcd/pkg/git"
	"github.com/go-logr/logr"
	"github.com/google/go-github/v47/github"
	"golang.org/x/oauth2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weaveworks/pipeline-controller/server"
)

type SSHCredentials struct {
	identity   []byte
	knownHosts []byte
	password   []byte
}

type GitHubPR struct {
	ghTokenFetcher        func(ctx context.Context, c client.Client) (string, error)
	sshCredentialsFetcher func(ctx context.Context) (*SSHCredentials, error)
	c                     client.Client
	log                   logr.Logger
}

func New(opts ...Opt) (*GitHubPR, error) {
	g := &GitHubPR{}
	for _, opt := range opts {
		if err := opt(g); err != nil {
			return nil, err
		}
	}

	return g, nil
}

func (g GitHubPR) Promote(ctx context.Context, promotion server.Promotion) (*server.PromotionResult, error) {
	cloneDir, err := os.MkdirTemp("", "promotion-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary clone dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(cloneDir); err != nil {
			g.log.Error(err, "failed cleaning up clone dir")
		}
	}()

	gitClient, err := g.cloneRepo(ctx, promotion.Environment.GitSpec.URL, cloneDir)
	if err != nil {
		return nil, fmt.Errorf("failed to clone repo: %w", err)
	}
	headBranch := fmt.Sprintf("promotion-%s-%s-%s", promotion.AppNS, promotion.AppName, promotion.Environment.Name)
	if err := gitClient.SwitchBranch(ctx, headBranch); err != nil {
		return nil, fmt.Errorf("failed to switch branch: %w", err)
	}

	// set version in manifests

	if err := g.patchManifests(cloneDir, promotion); err != nil {
		return nil, fmt.Errorf("failed to patch manifest files: %w", err)
	}

	clean, err := gitClient.IsClean()
	if err != nil {
		return nil, fmt.Errorf("failed to determine worktree state: %w", err)
	}
	if clean {
		// TODO: rw.WriteHeader(http.StatusNoContent)
		g.log.Info("nothing to commit")
		return &server.PromotionResult{}, nil
	}

	// commit

	commit, err := gitClient.Commit(git.Commit{
		Message: "promoting version",
		Author: git.Signature{
			Name: "Promotion Server",
			When: time.Now(),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to commit manifests: %w", err)
	}
	g.log.Info("committed patched manifests", "commit", commit)

	// push to remote

	if err := gitClient.Push(ctx); err != nil {
		return nil, fmt.Errorf("failed to push changes: %w", err)
	}
	g.log.Info("pushed promotion branch")

	// create PR

	pr, err := g.createPullRequest(ctx, headBranch, promotion.Environment.GitSpec.URL, promotion)
	if err != nil {
		return nil, fmt.Errorf("failed to create PR: %w", err)
	}
	g.log.Info("created PR", "pr", pr.HTMLURL)

	return &server.PromotionResult{
		Location: *pr.HTMLURL,
	}, nil
}

func (s GitHubPR) createPullRequest(ctx context.Context, head, gitURL string, promotion server.Promotion) (*github.PullRequest, error) {
	ghToken, err := s.ghTokenFetcher(ctx, s.c)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GitHub token: %w", err)
	}
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{
			AccessToken: ghToken,
		},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	newPR := &github.NewPullRequest{
		Title: github.String(fmt.Sprintf("Promote %s/%s in %s to %s",
			promotion.AppNS, promotion.AppName, promotion.Environment.Name, promotion.Version)),
		Head:                github.String(head),
		Base:                github.String("main"),
		Body:                nil,
		MaintainerCanModify: github.Bool(true),
	}

	org, repo, err := parseGHURL(gitURL)
	if err != nil {
		return nil, fmt.Errorf("failed parsing GitHub URL: %w", err)
	}

	pr, _, err := client.PullRequests.Create(ctx, org, repo, newPR)
	if err != nil {
		if strings.Contains(err.Error(), "A pull request already exists") {
			existingPRs, _, listErr := client.PullRequests.List(ctx, org, repo, &github.PullRequestListOptions{
				Head: head,
			})
			if listErr != nil {
				return nil, fmt.Errorf("failed to search for existing PR: %w", listErr)
			}
			var existingPR *github.PullRequest
			// found one or more existing PRs, select the first one matching the head branch
			for _, epr := range existingPRs {
				epr := epr
				if epr.Head != nil && epr.Head.Ref != nil && *epr.Head.Ref == head {
					existingPR = epr
				}
			}
			if existingPR == nil {
				prNumbers := make([]int, len(existingPRs))
				for idx, existingPR := range existingPRs {
					prNumbers[idx] = *existingPR.Number
				}
				return nil, fmt.Errorf("failed to identify existing PR in list of %d PRs for %s: %#v", len(existingPRs), head, prNumbers)
			}
			existingPR.Title = newPR.Title
			pr, _, err := client.PullRequests.Edit(ctx, org, repo, *existingPR.Number, existingPR)
			if err != nil {
				return nil, fmt.Errorf("failed to update existing PR: %w", err)
			}
			return pr, nil
		}
		return nil, fmt.Errorf("failed to issue PR creation request: %w", err)
	}

	return pr, nil
}

func parseGHURL(u string) (string, string, error) {
	parsedURL, err := url.Parse(u)
	if err != nil {
		return "", "", fmt.Errorf("failed parsing URL: %w", err)
	}
	return path.Base(path.Dir(parsedURL.Path)), strings.TrimSuffix(path.Base(parsedURL.Path), ".git"), nil
}
