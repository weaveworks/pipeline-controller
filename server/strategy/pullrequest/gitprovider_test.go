package pullrequest

import (
	"github.com/fluxcd/go-git-providers/gitprovider"
	"github.com/onsi/gomega"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"testing"
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

func Test_newGitProviderClientFactory(t *testing.T) {
	tests := []struct {
		name             string
		in               GitProviderConfig
		expectedProvider gitprovider.ProviderID
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
			"github",
			"github.com",
			"",
		},
		{
			"can create git provider for github enterprise",
			GitProviderConfig{
				Type:      v1alpha1.Github,
				TokenType: "oauth2",
				Token:     "asdf",
				Hostname:  "github.myenterprise.com",
			},
			"github",
			"github.myenterprise.com",
			"",
		},
		{
			"can create git provider for gitlab.com",
			GitProviderConfig{
				Type:      v1alpha1.Gitlab,
				TokenType: "oauth2",
				Token:     "abc",
			},
			"gitlab",
			"https://gitlab.com", //TODO: raise issue with ggp cause the domain returns an URL
			"",
		},
		{
			"can create git provider for gitlab on prem",
			GitProviderConfig{
				Type:      v1alpha1.Gitlab,
				TokenType: "oauth2",
				Token:     "abc",
				Hostname:  "gitlab.myenterprise.com",
			},
			"gitlab",
			"https://gitlab.myenterprise.com", //TODO: raise issue with ggp cause the domain returns an URL
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testingutils.NewGomegaWithT(t)
			client, err := NewGitProviderClientFactory()(tt.in)
			if tt.errPattern != "" {
				g.Expect(err).To(gomega.MatchError(gomega.MatchRegexp(tt.errPattern)))
			} else {
				g.Expect(err).To(gomega.BeNil())
				g.Expect(client.ProviderID()).To(gomega.Equal(tt.expectedProvider))
				g.Expect(client.SupportedDomain()).To(gomega.Equal(tt.expectedDomain))
			}
		})
	}
}
