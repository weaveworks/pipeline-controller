package pullrequest

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	fgit "github.com/fluxcd/pkg/git"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/git"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

type PullRequest struct {
	c                client.Client
	log              logr.Logger
	gitClientFactory GitProviderClientFactory
}

var (
	_ strategy.Strategy = PullRequest{}

	ErrSpecIsNil = fmt.Errorf("PullRequest spec in Pipeline is nil")
)

func New(c client.Client, log logr.Logger, opts ...Opt) (*PullRequest, error) {
	g := &PullRequest{
		c:   c,
		log: log,
	}

	for _, opt := range opts {
		if err := opt(g); err != nil {
			return nil, err
		}
	}
	setDefaults(g)

	return g, nil
}

func setDefaults(g *PullRequest) {
	if g.gitClientFactory == nil {
		g.gitClientFactory = NewGitProviderClientFactory(g.log)
	}
}

func (g PullRequest) Handles(p pipelinev1alpha1.Promotion) bool {
	return p.Strategy.PullRequest != nil
}

func (g PullRequest) Promote(ctx context.Context, promSpec pipelinev1alpha1.Promotion, promotion strategy.Promotion) (*strategy.PromotionResult, error) {
	log := g.log.WithValues("promotion", promotion)

	prSpec := promSpec.Strategy.PullRequest
	if prSpec == nil {
		return nil, ErrSpecIsNil
	}

	_, err := gitProviderIsValid(prSpec.Type)
	if err != nil {
		return nil, fmt.Errorf("invalid git provider type: %w", err)
	}

	if prSpec.Type == "" {
		return nil, ErrGitProviderTypeEmpty
	}

	cloneDir, err := os.MkdirTemp("", "promotion-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary clone dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(cloneDir); err != nil {
			log.Error(err, "failed cleaning up clone dir")
		}
	}()

	creds, err := g.fetchCredentials(ctx, g.c, promotion.PipelineNamespace, prSpec.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch credentials: %w", err)
	}

	gitClient, err := g.cloneRepo(ctx, *prSpec, cloneDir, creds)
	if err != nil {
		return nil, fmt.Errorf("failed to clone repo: %w", err)
	}
	headBranch := fmt.Sprintf("promotion-%s-%s-%s", promotion.PipelineNamespace, promotion.PipelineName, promotion.Environment.Name)
	if err := gitClient.SwitchBranch(ctx, headBranch); err != nil {
		return nil, fmt.Errorf("failed to switch branch: %w", err)
	}

	if err := g.patchManifests(cloneDir, promotion); err != nil {
		return nil, fmt.Errorf("failed to patch manifest files: %w", err)
	}

	clean, err := gitClient.IsClean()
	if err != nil {
		return nil, fmt.Errorf("failed to determine worktree state: %w", err)
	}
	if clean {
		log.Info("nothing to commit")
		return &strategy.PromotionResult{}, nil
	}

	commit, err := gitClient.Commit(fgit.Commit{
		Message: "promoting version",
		Author: fgit.Signature{
			Name: "Promotion Server",
			When: time.Now(),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to commit manifests: %w", err)
	}
	log.Info("committed patched manifests", "commit", commit)

	if err := gitClient.Push(ctx); err != nil {
		return nil, fmt.Errorf("failed to push changes: %w", err)
	}
	log.Info("pushed promotion branch")

	pr, err := g.createPullRequest(ctx, string(creds["token"]), headBranch, prSpec.BaseBranch, prSpec.Type, prSpec.URL, promotion)
	if err != nil {
		return nil, fmt.Errorf("failed to create PR: %w", err)
	}
	log.Info("created PR", "pr", pr.Link)

	return &strategy.PromotionResult{
		Location: pr.Link,
	}, nil
}

func (g PullRequest) fetchCredentials(ctx context.Context, c client.Client, ns string, secretRef meta.LocalObjectReference) (map[string][]byte, error) {
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: secretRef.Name}, &secret); err != nil {
		return nil, fmt.Errorf("failed to fetch Secret: %w", err)
	}
	return secret.Data, nil
}

func (s PullRequest) createPullRequest(ctx context.Context, token string, head string, baseBranch string, gitProviderType pipelinev1alpha1.GitProviderType, gitURL string, promotion strategy.Promotion) (*git.PullRequest, error) {
	if token == "" {
		return nil, ErrTokenIsEmpty
	}

	var (
		err error
	)

	provider := GitProviderConfig{
		Token:            token,
		TokenType:        "oauth2",
		Type:             gitProviderType,
		Domain:           "",
		DestructiveCalls: false,
	}

	parsedURL, err := git.ParseURL(string(gitProviderType), gitURL)
	if err != nil {
		return nil, fmt.Errorf("failed parsing git provider URL: %w", err)
	}

	provider.Domain = parsedURL.Domain

	client, err := s.gitClientFactory(provider)
	if err != nil {
		return nil, fmt.Errorf("failed creating git provider client: %w", err)
	}

	newTitle := fmt.Sprintf(
		"Promote %s/%s in %s to %s",
		promotion.PipelineNamespace,
		promotion.PipelineName,
		promotion.Environment.Name,
		promotion.Version,
	)
	prDesc := fmt.Sprintf(`<details>
<summary>metadata</summary>
!!! DO NOT EDIT !!!

%s/%s/%s
</details>`,
		promotion.PipelineNamespace,
		promotion.PipelineName,
		promotion.Environment.Name,
	)

	pr, err := client.CreatePullRequest(ctx, git.PullRequestInput{
		RepositoryURL: gitURL,
		Title:         newTitle,
		Body:          prDesc,
		Head:          head,
		Base:          baseBranch,
		Commits:       []git.Commit{},
	})
	if err == nil {
		return pr, nil
	}

	prList, err := client.ListPullRequests(ctx, gitURL)
	if err != nil {
		return nil, fmt.Errorf("failed listing PRs: %w", err)
	}

	var existingPRNo *int
	for _, existingPR := range prList {
		if existingPR.Source == head {
			no := existingPR.Number
			existingPRNo = &no
			break // we found a matching PR, no more iteration necessary
		}
	}

	if existingPRNo == nil {
		return nil, fmt.Errorf("failed to create or find existing PR: headBranch=%s baseBranch=%s", head, baseBranch)
	}

	pr, err = client.UpdatePullRequest(ctx, gitURL, *existingPRNo, git.UpdatePullRequestOptions{
		Title: newTitle,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update existing PR: %w", err)
	}

	return pr, nil
}
