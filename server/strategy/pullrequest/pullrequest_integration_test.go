//go:build integration

package pullrequest

import (
	"context"
	"fmt"
	"github.com/fluxcd/go-git-providers/gitprovider"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/onsi/gomega"
	"github.com/stretchr/testify/require"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"github.com/weaveworks/pipeline-controller/server/strategy"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

type Config struct {
	gitProviderConfig GitProviderConfig
	username          string
}

func TestPromote_Integration(t *testing.T) {

	releaseManifest := `
apiVersion: helm.toolkit.fluxcd.io/v2beta1
kind: HelmRelease
metadata:
  name: podinfo
spec:
  interval: 5m
  chart:
    spec:
      chart: podinfo
      version: "5.0.0" # {"$promotion": "foo:bar:prod"}
      sourceRef:
        kind: HelmRepository
        name: podinfo
      interval: 1m
`

	githubConfig := gitProviderConfig(t, v1alpha1.Github, "GITHUB_USER", "GITHUB_TOKEN")
	gitlabConfig := gitProviderConfig(t, v1alpha1.Gitlab, "GITLAB_USER", "GITLAB_TOKEN")

	tests := []struct {
		name       string
		config     Config
		promSpec   v1alpha1.Promotion
		promotion  strategy.Promotion
		apiObjects []client.Object
		err        error
		errPattern string
	}{
		{
			"can promote github",
			githubConfig,
			v1alpha1.Promotion{
				PullRequest: &v1alpha1.PullRequestPromotion{
					Type: v1alpha1.Github,
					SecretRef: meta.LocalObjectReference{
						Name: "repo-credentials",
					},
				},
			},
			strategy.Promotion{
				PipelineNamespace: "foo",
				PipelineName:      "bar",
				Environment: v1alpha1.Environment{
					Name: "prod",
					Targets: []v1alpha1.Target{
						{
							Namespace: "default",
						},
					},
				},
				Version: "6.0.0",
			},
			[]client.Object{
				&corev1.Secret{
					ObjectMeta: v1.ObjectMeta{
						Namespace: "foo",
						Name:      "repo-credentials",
					},
					Data: map[string][]byte{
						"token":    []byte(githubConfig.gitProviderConfig.Token),
						"username": []byte(githubConfig.username),
						"password": []byte(githubConfig.gitProviderConfig.Token),
					},
				},
			},
			nil,
			"",
		},
		{
			"can promote gitlab",
			gitlabConfig,
			v1alpha1.Promotion{
				PullRequest: &v1alpha1.PullRequestPromotion{
					Type: v1alpha1.Gitlab,
					SecretRef: meta.LocalObjectReference{
						Name: "repo-credentials",
					},
				},
			},
			strategy.Promotion{
				PipelineNamespace: "foo",
				PipelineName:      "bar",
				Environment: v1alpha1.Environment{
					Name: "prod",
					Targets: []v1alpha1.Target{
						{
							Namespace: "default",
						},
					},
				},
				Version: "6.0.0",
			},
			[]client.Object{
				&corev1.Secret{
					ObjectMeta: v1.ObjectMeta{
						Namespace: "foo",
						Name:      "repo-credentials",
					},
					Data: map[string][]byte{
						"token":    []byte(gitlabConfig.gitProviderConfig.Token),
						"username": []byte(gitlabConfig.username),
						"password": []byte(gitlabConfig.gitProviderConfig.Token),
					},
				},
			},
			nil,
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			g := testingutils.NewGomegaWithT(t)
			fc := fake.NewClientBuilder().WithObjects(tt.apiObjects...).Build()
			strat, err := New(fc, logger.NewLogger(logger.Options{}))
			if err != nil {
				t.Fatalf("unable to create pullrequest promotion strategy: %s", err)
			}

			gitClient, _ := newGitProviderClientFactory()(tt.config.gitProviderConfig)

			require.Nil(t, err)

			userRepo, err := createTestRepo(tt.config, gitClient)
			require.Nil(t, err)

			path := "release.yaml"
			files := []gitprovider.CommitFile{
				{
					Path:    &path,
					Content: &releaseManifest,
				},
			}

			_, err = userRepo.Commits().Create(context.Background(), "main", "resource integration test", files)
			require.Nil(t, err)

			t.Cleanup(func() {
				err := userRepo.Delete(context.Background())
				require.Nil(t, err)
			})

			tt.promSpec.PullRequest.URL = userRepo.Repository().GetCloneURL(gitprovider.TransportTypeHTTPS)

			res, err := strat.Promote(context.Background(), tt.promSpec, tt.promotion)
			if tt.err != nil {
				g.Expect(err).To(gomega.Equal(tt.err))
			} else if tt.errPattern != "" {
				g.Expect(err).To(gomega.MatchError(gomega.MatchRegexp(tt.errPattern)))
			} else {
				g.Expect(err).To(gomega.BeNil())
				g.Expect(res).NotTo(gomega.BeNil())
			}
		})
	}
}

func createTestRepo(config Config, client gitprovider.Client) (gitprovider.UserRepository, error) {
	repoName := fmt.Sprintf("test-%d", rand.Int())
	repoRef := gitprovider.UserRepositoryRef{
		UserRef: gitprovider.UserRef{
			Domain:    client.SupportedDomain(),
			UserLogin: config.username,
		},
		RepositoryName: repoName,
	}
	ctx := context.Background()
	return client.UserRepositories().Create(ctx, repoRef, gitprovider.RepositoryInfo{}, &gitprovider.RepositoryCreateOptions{
		AutoInit:        gitprovider.BoolVar(true),
		LicenseTemplate: gitprovider.LicenseTemplateVar(gitprovider.LicenseTemplateApache2),
	})
}

func gitProviderConfig(t *testing.T, gitProviderType v1alpha1.GitProviderType, userEnv string, tokenEnv string) Config {

	username := os.Getenv(userEnv)
	require.NotEmpty(t, username)
	token := os.Getenv(tokenEnv)
	require.NotEmpty(t, token)

	return Config{
		username: username,
		gitProviderConfig: GitProviderConfig{
			Token:            token,
			Type:             gitProviderType,
			TokenType:        "oauth2",
			DestructiveCalls: true,
		},
	}
}
