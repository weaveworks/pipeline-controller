package githubpr

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/git"
	"github.com/go-logr/logr"
	"github.com/google/go-github/v47/github"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

type GitHubPR struct {
	c                   client.Client
	log                 logr.Logger
	githubClientFactory ClientFactory
}

var (
	_ strategy.Strategy = GitHubPR{}

	ErrSpecIsNil    = fmt.Errorf("PullRequest spec in Pipeline is nil")
	ErrTokenIsEmpty = fmt.Errorf("GitHub token is empty")
)

func NewGitHubPR(c client.Client, log logr.Logger, opts ...Opt) (*GitHubPR, error) {
	g := &GitHubPR{
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

func setDefaults(g *GitHubPR) {
	if g.githubClientFactory == nil {
		g.githubClientFactory = func(c *http.Client) GitHubClient {
			ghc := github.NewClient(c)
			return DelegatingGitHubClient{ghc}
		}
	}
}

func (g GitHubPR) Handles(p pipelinev1alpha1.Promotion) bool {
	return p.PullRequest != nil
}

func (g GitHubPR) Promote(ctx context.Context, promSpec pipelinev1alpha1.Promotion, promotion strategy.Promotion) (*strategy.PromotionResult, error) {
	log := g.log.WithValues("promotion", promotion)

	prSpec := promSpec.PullRequest
	if prSpec == nil {
		return nil, ErrSpecIsNil
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
	log.Info("committed patched manifests", "commit", commit)

	if err := gitClient.Push(ctx); err != nil {
		return nil, fmt.Errorf("failed to push changes: %w", err)
	}
	log.Info("pushed promotion branch")

	pr, err := g.createPullRequest(ctx, string(creds["token"]), headBranch, prSpec.URL, promotion)
	if err != nil {
		return nil, fmt.Errorf("failed to create PR: %w", err)
	}
	log.Info("created PR", "pr", pr.HTMLURL)

	return &strategy.PromotionResult{
		Location: *pr.HTMLURL,
	}, nil
}

func (g GitHubPR) fetchCredentials(ctx context.Context, c client.Client, ns string, secretRef meta.LocalObjectReference) (map[string][]byte, error) {
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: secretRef.Name}, &secret); err != nil {
		return nil, fmt.Errorf("failed to fetch Secret: %w", err)
	}
	return secret.Data, nil
}
