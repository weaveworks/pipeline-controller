package githubpr

import (
	"fmt"

	"github.com/fluxcd/pkg/git"
	"github.com/fluxcd/pkg/git/gogit"
	"golang.org/x/net/context"
)

func (s GitHubPR) cloneRepo(ctx context.Context, url, dir string) (*gogit.Client, error) {
	sshCredentials, err := s.sshCredentialsFetcher(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch SSH credentials: %w", err)
	}
	c, err := gogit.NewClient(dir, &git.AuthOptions{
		Transport:  git.SSH,
		Username:   "git",
		Identity:   sshCredentials.identity,
		Password:   string(sshCredentials.password),
		KnownHosts: sshCredentials.knownHosts,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Git client: %w", err)
	}
	defer c.Close()
	commit, err := c.Clone(ctx,
		url,
		git.CloneOptions{
			ShallowClone: false,
			CheckoutStrategy: git.CheckoutStrategy{
				Branch: "main",
			},
		})
	if err != nil {
		return nil, fmt.Errorf("failed to clone Git repository: %w", err)
	}

	s.log.Info("cloned repository", "commit", commit.String())
	return c, nil
}
