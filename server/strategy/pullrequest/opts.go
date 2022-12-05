package pullrequest

type Opt func(g *PullRequest) error

func GitClientFactory(cf GitProviderClientFactory) Opt {
	return func(g *PullRequest) error {
		g.gitClientFactory = cf
		return nil
	}
}
