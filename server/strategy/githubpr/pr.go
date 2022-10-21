package githubpr

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/fluxcd/go-git-providers/gitprovider"
	"github.com/google/go-github/v47/github"
	"golang.org/x/oauth2"

	"github.com/weaveworks/pipeline-controller/server/strategy"
)

type PullRequestClient interface {
	Create(ctx context.Context, owner string, repo string, pull *github.NewPullRequest) (*github.PullRequest, *github.Response, error)
	List(ctx context.Context, owner string, repo string, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error)
	Edit(ctx context.Context, owner string, repo string, number int, pull *github.PullRequest) (*github.PullRequest, *github.Response, error)
}

type GitHubClient interface {
	PullRequests() PullRequestClient
}

type ClientFactory func(*http.Client) GitHubClient

type DelegatingGitHubClient struct {
	c *github.Client
}

func (d DelegatingGitHubClient) PullRequests() PullRequestClient {
	return d
}

func (d DelegatingGitHubClient) Create(ctx context.Context, owner string, repo string, pull *github.NewPullRequest) (*github.PullRequest, *github.Response, error) {
	return d.c.PullRequests.Create(ctx, owner, repo, pull)
}

func (d DelegatingGitHubClient) List(ctx context.Context, owner string, repo string, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error) {
	return d.c.PullRequests.List(ctx, owner, repo, opts)
}

func (d DelegatingGitHubClient) Edit(ctx context.Context, owner string, repo string, number int, pull *github.PullRequest) (*github.PullRequest, *github.Response, error) {
	return d.c.PullRequests.Edit(ctx, owner, repo, number, pull)
}

func (s GitHubPR) createPullRequest(ctx context.Context, token string, head, gitURL string, promotion strategy.Promotion) (*github.PullRequest, error) {
	if token == "" {
		return nil, ErrTokenIsEmpty
	}
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{
			AccessToken: token,
		},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := s.githubClientFactory(tc)

	newPR := &github.NewPullRequest{
		Title: github.String(fmt.Sprintf("Promote %s/%s in %s to %s",
			promotion.PipelineNamespace, promotion.PipelineName, promotion.Environment.Name, promotion.Version)),
		Head:                github.String(head),
		Base:                github.String("main"),
		Body:                nil,
		MaintainerCanModify: github.Bool(true),
	}

	userRepoRef, err := gitprovider.ParseUserRepositoryURL(gitURL)
	if err != nil {
		return nil, fmt.Errorf("failed parsing GitHub URL: %w", err)
	}

	pr, _, err := client.PullRequests().Create(ctx, userRepoRef.UserRef.UserLogin, userRepoRef.RepositoryName, newPR)
	if err != nil {
		if strings.Contains(err.Error(), "A pull request already exists") {
			existingPRs, _, listErr := client.PullRequests().List(
				ctx,
				userRepoRef.UserRef.UserLogin,
				userRepoRef.RepositoryName,
				&github.PullRequestListOptions{
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
			pr, _, err := client.PullRequests().Edit(ctx, userRepoRef.UserRef.UserLogin, userRepoRef.RepositoryName, *existingPR.Number, existingPR)
			if err != nil {
				return nil, fmt.Errorf("failed to update existing PR: %w", err)
			}
			return pr, nil
		}
		return nil, fmt.Errorf("failed to issue PR creation request: %w", err)
	}

	return pr, nil
}
