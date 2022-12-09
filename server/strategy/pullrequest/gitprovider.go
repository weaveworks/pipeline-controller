package pullrequest

import (
	"fmt"
	"github.com/fluxcd/go-git-providers/github"
	"github.com/fluxcd/go-git-providers/gitlab"
	"github.com/fluxcd/go-git-providers/gitprovider"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

var (
	ErrGitProviderTypeEmpty     = fmt.Errorf("git provider type is empty")
	ErrGitProviderTypeInvalid   = fmt.Errorf("git provider type not supported")
	ErrGitProviderDomainIsEmpty = fmt.Errorf("git provider host is empty")
	ErrTokenTypeIsEmpty         = fmt.Errorf("git provider token type is empty")
	ErrTokenIsEmpty             = fmt.Errorf("git provider token is empty")
)

type GitProviderClientFactory func(provider GitProviderConfig) (gitprovider.Client, error)

type GitProviderConfig struct {
	Token            string
	TokenType        string
	Type             v1alpha1.GitProviderType
	Hostname         string
	DestructiveCalls bool
}

// same as https://github.com/weaveworks/weave-gitops-enterprise/blob/7ef05e773d7650a83cfa86dbd642253353b584c0/cmd/clusters-service/pkg/git/git.go#L286
func newGitProviderClientFactory() GitProviderClientFactory {
	return func(provider GitProviderConfig) (gitprovider.Client, error) {
		var client gitprovider.Client
		var err error

		clientOptions := []gitprovider.ClientOption{}

		if provider.Type == "" {
			return nil, ErrGitProviderTypeEmpty
		}

		if provider.TokenType == "" {
			return nil, ErrTokenTypeIsEmpty
		}

		if provider.Token == "" {
			return nil, ErrTokenIsEmpty
		}

		if provider.TokenType != "oauth2" {
			return nil, fmt.Errorf("git provider token type is invalid %s", provider.TokenType)
		}

		clientOptions = append(clientOptions, gitprovider.WithOAuth2Token(provider.Token))

		if provider.DestructiveCalls {
			//TODO bring visibility to this action
			clientOptions = append(clientOptions, gitprovider.WithDestructiveAPICalls(provider.DestructiveCalls))
		}

		if provider.Hostname != "" {
			clientOptions = append(clientOptions, gitprovider.WithDomain(provider.Hostname))
		}

		switch provider.Type {
		case v1alpha1.Github:
			client, err = github.NewClient(clientOptions...)
			if err != nil {
				return nil, err
			}
		case v1alpha1.Gitlab:
			client, err = gitlab.NewClient(provider.Token, provider.TokenType, clientOptions...)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("the Git provider %q is not supported", provider.Type)
		}
		return client, err
	}

}

func gitProviderIsValid(gitProviderType v1alpha1.GitProviderType) (bool, error) {

	if gitProviderType == "" {
		return false, ErrGitProviderTypeEmpty
	}

	switch gitProviderType {
	case v1alpha1.Github:
	case v1alpha1.Gitlab:
		return true, nil
	default:
		return false, fmt.Errorf("the Git provider %q is not supported", gitProviderType)
	}
	return true, nil
}
