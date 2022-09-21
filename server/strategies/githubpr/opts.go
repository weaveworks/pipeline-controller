package githubpr

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Opt func(g *GitHubPR) error

func GitHubTokenSecret(ns, name string) Opt {
	return func(s *GitHubPR) error {
		var ghToken string
		s.ghTokenFetcher = func(ctx context.Context, c client.Client) (string, error) {
			if ghToken != "" {
				return ghToken, nil
			}
			var secret corev1.Secret
			if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &secret); err != nil {
				return "", fmt.Errorf("failed to fetch Secret: %w", err)
			}
			token, ok := secret.Data["token"]
			if !ok {
				return "", ErrMissingSecretField{fieldName: "token"}
			}
			ghToken = string(token)
			return ghToken, nil
		}

		return nil
	}
}

func Logger(l logr.Logger) Opt {
	return func(s *GitHubPR) error {
		s.log = l
		return nil
	}
}

func Client(c client.Client) Opt {
	return func(s *GitHubPR) error {
		s.c = c
		return nil
	}
}

func SSHCredentialsSecret(ns, name string) Opt {
	return func(s *GitHubPR) error {
		var sshCredentials *SSHCredentials
		s.sshCredentialsFetcher = func(ctx context.Context) (*SSHCredentials, error) {
			if sshCredentials != nil {
				return sshCredentials, nil
			}
			var secret corev1.Secret
			if err := s.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &secret); err != nil {
				return nil, fmt.Errorf("failed to fetch Secret: %w", err)
			}
			identity, ok := secret.Data["identity"]
			if !ok {
				return nil, ErrMissingSecretField{fieldName: "identity"}
			}
			knownHosts, ok := secret.Data["known_hosts"]
			if !ok {
				return nil, ErrMissingSecretField{fieldName: "known_hosts"}
			}
			password := secret.Data["password"]
			return &SSHCredentials{
				identity:   identity,
				knownHosts: knownHosts,
				password:   password,
			}, nil
		}

		return nil
	}
}
