package pullrequest

import (
	"context"
	"fmt"
	"net/url"

	"github.com/fluxcd/pkg/git"
	"github.com/fluxcd/pkg/git/gogit"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

func (s PullRequest) cloneRepo(ctx context.Context, prSpec v1alpha1.PullRequestPromotion, dir string, credentials map[string][]byte) (git.RepositoryClient, error) {
	u, err := url.Parse(prSpec.URL)
	if err != nil {
		return nil, fmt.Errorf("failed parsing URL from spec: %w", err)
	}

	authOpts, err := git.NewAuthOptions(*u, credentials)
	if err != nil {
		return nil, fmt.Errorf("failed configuring auth opts for repo URL %q: %w", u.String(), err)
	}

	c, err := gogit.NewClient(dir, authOpts)
	if err != nil {
		return nil, fmt.Errorf("failed creating git client: %w", err)
	}
	defer c.Close()

	cloneOpts := git.CloneOptions{
		RecurseSubmodules: false,
		ShallowClone:      false,
		CheckoutStrategy: git.CheckoutStrategy{
			Branch: prSpec.BaseBranch,
		},
	}

	_, err = c.Clone(ctx, prSpec.URL, cloneOpts)
	if err != nil {
		return nil, fmt.Errorf("failed cloning repository: %w", err)
	}

	return c, nil
}
