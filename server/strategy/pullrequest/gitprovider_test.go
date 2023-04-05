package pullrequest

import (
	"testing"

	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/onsi/gomega"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/git"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
)

func Test_gitProviderIsEmpty(t *testing.T) {
	tests := []struct {
		name       string
		in         v1alpha1.GitProviderType
		isValid    bool
		errPattern string
	}{
		{
			"empty is invalid",
			"",
			false,
			"git provider type is empty",
		},
		{
			"subversion is invalid",
			"subversion",
			false,
			"the Git provider \"subversion\" is not supported",
		},
		{
			"github is valid",
			"github",
			true,
			"",
		},
		{
			"gitlab is valid",
			"gitlab",
			true,
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testingutils.NewGomegaWithT(t)
			isValid, err := gitProviderIsValid(tt.in)
			g.Expect(isValid).To(gomega.Equal(tt.isValid))
			if tt.errPattern != "" {
				g.Expect(err).To(gomega.MatchError(gomega.MatchRegexp(tt.errPattern)))
			} else {
				g.Expect(err).To(gomega.BeNil())
			}
		})
	}
}

func TestNewGitProviderClientFactory(t *testing.T) {
	tests := []struct {
		name             string
		in               GitProviderConfig
		expectedProvider string
		expectedDomain   string
		errPattern       string
	}{
		{
			"cannot create empty git provider",
			GitProviderConfig{},
			"",
			"",
			"git provider type is empty",
		},
		{
			"cannot create git provider without type",
			GitProviderConfig{
				Type: "",
			},
			"",
			"",
			"git provider type is empty",
		},
		{
			"cannot create git provider without token type",
			GitProviderConfig{
				Type:      v1alpha1.Github,
				TokenType: "",
			},
			"",
			"",
			"git provider token type is empty",
		},
		{
			"cannot create git provider without token",
			GitProviderConfig{
				Type:      v1alpha1.Github,
				TokenType: "oauth2",
				Token:     "",
			},
			"",
			"",
			"git provider token is empty",
		},
		{
			"cannot create git provider for not supported provider",
			GitProviderConfig{
				Type:      "dontExistGitProvider",
				TokenType: "oauth2",
				Token:     "asdf",
			},
			"",
			"",
			"the Git provider \"dontExistGitProvider\" is not supported",
		},
		{
			"can create git provider for github.com",
			GitProviderConfig{
				Type:      v1alpha1.Github,
				TokenType: "oauth2",
				Token:     "asdf",
			},
			git.GitHubProviderName,
			"github.com",
			"",
		},
		{
			"can create git provider for github enterprise",
			GitProviderConfig{
				Type:      v1alpha1.Github,
				TokenType: "oauth2",
				Token:     "asdf",
				Domain:    "github.myenterprise.com",
			},
			git.GitHubProviderName,
			"https://github.myenterprise.com",
			"",
		},
		{
			"can create git provider for gitlab.com",
			GitProviderConfig{
				Type:      v1alpha1.Gitlab,
				TokenType: "oauth2",
				Token:     "abc",
			},
			git.GitLabProviderName,
			"https://gitlab.com",
			"",
		},
		{
			"can create git provider for gitlab enterprise",
			GitProviderConfig{
				Type:      v1alpha1.Gitlab,
				TokenType: "oauth2",
				Token:     "abc",
				Domain:    "gitlab.myenterprise.com",
			},
			git.GitLabProviderName,
			"https://gitlab.myenterprise.com",
			"",
		},
		{
			"can create git provider for azure devops",
			GitProviderConfig{
				Type:      v1alpha1.AzureDevOps,
				TokenType: "oauth2",
				Token:     "abc",
			},
			git.GitHubProviderName,
			"", // There is no "SupportedDomain" for jenkins-x/go-scm.
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testingutils.NewGomegaWithT(t)
			client, err := NewGitProviderClientFactory(logger.NewLogger(logger.Options{}))(tt.in)
			if tt.errPattern != "" {
				g.Expect(err).To(gomega.MatchError(gomega.MatchRegexp(tt.errPattern)))
			} else {
				g.Expect(err).To(gomega.BeNil())
				g.Expect(client.Name()).To(gomega.BeAssignableToTypeOf(tt.expectedProvider))
				// TODO?
				g.Expect(client.SupportedDomain()).To(gomega.Equal(tt.expectedDomain))
			}
		})
	}
}

func Test_decorateCustomDomainByType(t *testing.T) {

	tests := []struct {
		name            string
		gitProviderType v1alpha1.GitProviderType
		domain          string
		expectedDomain  string
		errPattern      string
	}{
		{
			"cannot decorate if empty git provider",
			"",
			"",
			"",
			ErrGitProviderTypeEmpty.Error(),
		},
		{
			"cannot decorate if empty domain",
			v1alpha1.Github,
			"",
			"",
			ErrGitProviderDomainIsEmpty.Error(),
		},
		{
			"decorate non-gitlab enterprise domain has no effect",
			v1alpha1.Github,
			"github.myenterprise.com",
			"github.myenterprise.com",
			"",
		},
		{
			"decorate gitlab enterprise domain adds https scheme",
			v1alpha1.Gitlab,
			"gitlab.git.dev.weave.works",
			"https://gitlab.git.dev.weave.works",
			"",
		},
		{
			"decorate gitlab enterprise with https scheme does not change",
			v1alpha1.Gitlab,
			"https://gitlab.git.dev.weave.works",
			"https://gitlab.git.dev.weave.works",
			"",
		},
		{
			"decorate gitlab enterprise with http scheme does not change",
			v1alpha1.Gitlab,
			"http://gitlab.git.dev.weave.works",
			"http://gitlab.git.dev.weave.works",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testingutils.NewGomegaWithT(t)
			domain, err := decorateCustomDomainByType(tt.gitProviderType, tt.domain)
			if tt.errPattern != "" {
				g.Expect(err).To(gomega.MatchError(gomega.MatchRegexp(tt.errPattern)))
			} else {
				g.Expect(domain).To(gomega.Equal(tt.expectedDomain))
				g.Expect(err).To(gomega.BeNil())
			}
		})
	}

}
