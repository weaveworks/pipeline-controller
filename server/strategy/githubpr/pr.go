package githubpr

import (
	"context"
	"fmt"

	"github.com/fluxcd/go-git-providers/gitprovider"

	"github.com/weaveworks/pipeline-controller/server/strategy"
)

type ClientFactory func(optFns ...gitprovider.ClientOption) (gitprovider.Client, error)

func (s GitHubPR) createPullRequest(ctx context.Context, token string, head, gitURL string, promotion strategy.Promotion) (gitprovider.PullRequest, error) {
	if token == "" {
		return nil, ErrTokenIsEmpty
	}

	userRepoRef, err := gitprovider.ParseUserRepositoryURL(gitURL)
	if err != nil {
		return nil, fmt.Errorf("failed parsing GitHub URL: %w", err)
	}

	client, err := s.gitClientFactory(gitprovider.WithOAuth2Token(token))
	if err != nil {
		return nil, fmt.Errorf("failed creating GitHub client: %w", err)
	}
	userRepo, err := client.UserRepositories().Get(ctx, *userRepoRef)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve repository: ")
	}

	newTitle := fmt.Sprintf("Promote %s/%s in %s to %s",
		promotion.PipelineNamespace, promotion.PipelineName, promotion.Environment.Name, promotion.Version)
	pr, err := userRepo.PullRequests().Create(
		ctx,
		newTitle,
		head,
		"main",
		"")

	if err != nil {
		prList, err := userRepo.PullRequests().List(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed listing PRs: %w", err)
		}

		var existingPRNo *int
		for _, existingPR := range prList {
			if existingPR.Get().SourceBranch == head {
				no := existingPR.Get().Number
				existingPRNo = &no
				break // we found a matching PR, no more iteration necessary
			}
		}

		if existingPRNo == nil {
			return nil, fmt.Errorf("failed to create PR: %w", err)
		}

		pr, err = userRepo.PullRequests().Edit(ctx, *existingPRNo, gitprovider.EditOptions{
			Title: &newTitle,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to update existing PR: %w", err)
		}
	}

	return pr, nil
}
