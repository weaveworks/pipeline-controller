package githubpr

type Opt func(g *GitHubPR) error

func GitHubClientFactory(cf ClientFactory) Opt {
	return func(g *GitHubPR) error {
		g.gitClientFactory = cf
		return nil
	}
}
