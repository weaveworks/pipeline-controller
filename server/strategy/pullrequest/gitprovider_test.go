package pullrequest

import (
	"github.com/fluxcd/go-git-providers/gitprovider"
	github2 "github.com/google/go-github/v47/github"
	"github.com/onsi/gomega"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"log"
	"net/url"
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

func TestNewGitProviderClientFactory(t *testing.T) {
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
			"gitlab",
			"https://gitlab.com", //TODO: change me when fixed https://github.com/fluxcd/go-git-providers/issues/175
			"",
		},
		{
			"can create git provider for gitlab enterprise",
			GitProviderConfig{
				Type:      v1alpha1.Gitlab,
				TokenType: "oauth2",
				Token:     "abc",
				Hostname:  "gitlab.myenterprise.com",
			},
			"gitlab",
			"https://gitlab.myenterprise.com", //TODO: change me when fixed https://github.com/fluxcd/go-git-providers/issues/175
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
				//assertRawClient(g, client, tt.in)
			}
		})
	}
}

func assertRawClient(g *gomega.WithT, client gitprovider.Client, gitProviderConfig GitProviderConfig) {
	var baseUrl *url.URL
	switch gitProviderConfig.Type {
	case v1alpha1.Github:
		g2 := client.Raw().(*github2.Client)
		baseUrl = g2.BaseURL
		if gitProviderConfig.Hostname != "" {
			g.Expect(gitProviderConfig.Hostname).To(gomega.Equal(baseUrl.Hostname()))
		}
		break
	case v1alpha1.Gitlab:
		//TODO given https://github.com/fluxcd/go-git-providers/issues/176
		// we cannot add the same assertions for gitlab than github so warning
		log.Println("[warning] I should have asserted gitlab baseUrl")
		break
	}

}
