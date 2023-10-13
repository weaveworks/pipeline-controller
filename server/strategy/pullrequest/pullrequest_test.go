package pullrequest_test

//go:generate mockgen -destination mock_gitprovider_test.go -package pullrequest_test github.com/fluxcd/go-git-providers/gitprovider Client,UserRepositoriesClient,UserRepository,PullRequestClient,PullRequest,OrgRepositoriesClient,OrgRepository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/weaveworks/pipeline-controller/server/strategy/pullrequest"

	"github.com/fluxcd/go-git-providers/gitprovider"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/gittestserver"
	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/git"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

type gitServerConfig struct {
	repoFixtureDir string
	username       string
	password       string
	publicKey      []byte
	privateKey     []byte
	ca             []byte
}

func initGitRepo(server *gittestserver.GitServer, fixture, branch, repositoryPath string) (*gogit.Repository, error) {
	fs := memfs.New()
	repo, err := gogit.Init(memory.NewStorage(), fs)
	if err != nil {
		return nil, err
	}

	err = commitFromFixture(repo, fixture)
	if err != nil {
		return nil, err
	}

	headRef, err := repo.Head()
	if err != nil {
		return nil, err
	}
	branchRef := plumbing.NewBranchReferenceName(branch)
	ref := plumbing.NewHashReference(branchRef, headRef.Hash())
	if err := repo.Storer.SetReference(ref); err != nil {
		return nil, err
	}

	if server.HTTPAddress() == "" {
		if err = server.StartHTTP(); err != nil {
			return nil, err
		}
		defer server.StopHTTP()
	}
	if _, err = repo.CreateRemote(&config.RemoteConfig{
		Name: gogit.DefaultRemoteName,
		URLs: []string{server.HTTPAddressWithCredentials() + repositoryPath},
	}); err != nil {
		return nil, err
	}

	if err = repo.Push(&gogit.PushOptions{
		RefSpecs: []config.RefSpec{"refs/heads/*:refs/heads/*"},
	}); err != nil {
		return nil, err
	}

	return repo, nil
}

func commitFromFixture(repo *gogit.Repository, fixture string) error {
	working, err := repo.Worktree()
	if err != nil {
		return err
	}
	fs := working.Filesystem

	if err = filepath.Walk(fixture, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fs.MkdirAll(fs.Join(path[len(fixture):]), info.Mode())
		}

		fileBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		ff, err := fs.Create(path[len(fixture):])
		if err != nil {
			return err
		}
		defer ff.Close()

		_, err = ff.Write(fileBytes)
		return err
	}); err != nil {
		return err
	}

	_, err = working.Add(".")
	if err != nil {
		return err
	}

	if _, err = working.Commit("Fixtures from "+fixture, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Jane Doe",
			Email: "jane@example.com",
			When:  time.Now(),
		},
	}); err != nil {
		return err
	}

	return nil
}

func initTestTLS() ([]byte, []byte, []byte) {
	tlsPublicKey, err := os.ReadFile("testdata/certs/server.pem")
	if err != nil {
		panic(err)
	}
	tlsPrivateKey, err := os.ReadFile("testdata/certs/server-key.pem")
	if err != nil {
		panic(err)
	}
	tlsCA, err := os.ReadFile("testdata/certs/ca.pem")
	if err != nil {
		panic(err)
	}

	return tlsPublicKey, tlsPrivateKey, tlsCA
}

func mockGitProviderFactory(c git.Provider) pullrequest.GitProviderClientFactory {
	return func(_ pullrequest.GitProviderConfig) (git.Provider, error) {
		return c, nil
	}
}

func TestHandles(t *testing.T) {
	tests := []struct {
		name     string
		in       v1alpha1.Promotion
		expected bool
	}{
		{
			"empty promotion",
			v1alpha1.Promotion{},
			false,
		},
		{
			"nil PullRequestPromotion",
			v1alpha1.Promotion{
				Strategy: v1alpha1.Strategy{
					PullRequest: nil,
				},
			},
			false,
		},
		{
			"empty PullRequestPromotion",
			v1alpha1.Promotion{
				Strategy: v1alpha1.Strategy{
					PullRequest: &v1alpha1.PullRequestPromotion{},
				},
			},
			true,
		},
	}

	strat, err := pullrequest.New(nil, logger.NewLogger(logger.Options{}))
	if err != nil {
		t.Fatalf("unable to create GitHub promotion strategy: %s", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testingutils.NewGomegaWithT(t)
			g.Expect(strat.Handles(tt.in)).To(Equal(tt.expected))
		})
	}
}

func TestPromote_no_pullrequest_spec(t *testing.T) {
	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: nil,
		},
	}
	promotion := strategy.Promotion{}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
		},
	)

	assert.Equal(t, pullrequest.ErrSpecIsNil, err)
	assert.Nil(t, res)
}

func TestPromote_no_secret(t *testing.T) {
	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				Type: "github",
			},
		},
	}
	promotion := strategy.Promotion{}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
		},
	)

	expectedError := "failed to fetch credentials: failed to fetch Secret: secrets \"\" not found"

	assert.Equal(t, expectedError, err.Error())
	assert.Nil(t, res)
}

func TestPromote_no_repo_url(t *testing.T) {
	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				Type: "github",
				SecretRef: meta.LocalObjectReference{
					Name: "repo-credentials",
				},
			},
		},
	}
	promotion := strategy.Promotion{
		PipelineNamespace: "foobar",
	}
	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "foobar",
				Name:      "repo-credentials",
			},
		},
	}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
			objects:       objects,
		},
	)

	expectedError := "failed to clone repo: failed configuring auth opts for repo URL \"\": no transport type set"

	assert.Equal(t, expectedError, err.Error())
	assert.Nil(t, res)
}

func TestPromote_invalid_repo_url(t *testing.T) {
	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				Type: "github",
				URL:  "https://idontexists",
				SecretRef: meta.LocalObjectReference{
					Name: "repo-credentials",
				},
			},
		},
	}
	promotion := strategy.Promotion{
		PipelineNamespace: "foobar",
	}
	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "foobar",
				Name:      "repo-credentials",
			},
		},
	}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
			objects:       objects,
		},
	)

	expectedError := "failed to clone repo: failed cloning repository: unable to clone 'https://idontexists'"

	assert.Contains(t, err.Error(), expectedError)
	assert.Nil(t, res)
}

func TestPromote_github_no_token(t *testing.T) {
	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				Type: "github",
				URL:  "to-be-filled-in-by-test-code",
				SecretRef: meta.LocalObjectReference{
					Name: "repo-credentials",
				},
			},
		},
	}
	promotion := strategy.Promotion{
		PipelineNamespace: "foo",
		PipelineName:      "bar",
		Environment: v1alpha1.Environment{
			Name: "dev",
		},
	}
	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "foo",
				Name:      "repo-credentials",
			},
		},
	}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
			objects:       objects,
			gitServerOpts: &gitServerConfig{repoFixtureDir: "testdata/git/repository"},
		},
	)

	expectedError := "failed to create PR: git provider token is empty"

	assert.Equal(t, expectedError, err.Error())
	assert.Nil(t, res)
}

func TestPromote_missing_or_invalid_credentials(t *testing.T) {
	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				Type: "github",
				URL:  "to-be-filled-in-by-test-code",
				SecretRef: meta.LocalObjectReference{
					Name: "repo-credentials",
				},
			},
		},
	}
	promotion := strategy.Promotion{
		PipelineNamespace: "foo",
		PipelineName:      "bar",
	}
	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "foo",
				Name:      "repo-credentials",
			},
		},
	}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
			objects:       objects,
			gitServerOpts: &gitServerConfig{
				repoFixtureDir: "testdata/git/repository",
				username:       "user",
				password:       "pass",
			},
		},
	)

	expectedError := "failed to clone repo: failed cloning repository: unable to clone '.*': authentication required"

	assert.Regexp(t, expectedError, err.Error())
	assert.Nil(t, res)
}

func TestPromote_http_not_supported(t *testing.T) {
	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				Type: "github",
				URL:  "to-be-filled-in-by-test-code",
				SecretRef: meta.LocalObjectReference{
					Name: "repo-credentials",
				},
			},
		},
	}
	promotion := strategy.Promotion{
		PipelineNamespace: "foo",
		PipelineName:      "bar",
		Environment: v1alpha1.Environment{
			Name: "dev",
		},
	}
	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "foo",
				Name:      "repo-credentials",
			},
			Data: map[string][]byte{
				"token":    []byte("token"),
				"username": []byte("user"),
				"password": []byte("pass"),
			},
		},
	}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
			objects:       objects,
			gitServerOpts: &gitServerConfig{
				repoFixtureDir: "testdata/git/repository",
				username:       "user",
				password:       "pass",
			},
		},
	)

	expectedError := "failed to create PR: failed parsing git provider URL: unsupported URL scheme, only HTTPS supported"

	assert.Contains(t, err.Error(), expectedError)
	assert.Nil(t, res)
}

func TestPromote_no_git_provider(t *testing.T) {
	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				URL: "to-be-filled-in-by-test-code",
				SecretRef: meta.LocalObjectReference{
					Name: "repo-credentials",
				},
			},
		},
	}
	promotion := strategy.Promotion{
		PipelineNamespace: "foo",
		PipelineName:      "bar",
		Environment: v1alpha1.Environment{
			Name: "dev",
		},
	}
	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "foo",
				Name:      "repo-credentials",
			},
			Data: map[string][]byte{
				"token":    []byte("token"),
				"username": []byte("user"),
				"password": []byte("pass"),
			},
		},
	}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
			objects:       objects,
			gitServerOpts: &gitServerConfig{
				repoFixtureDir: "testdata/git/repository",
				username:       "user",
				password:       "pass",
			},
		},
	)

	expectedError := "invalid git provider type: git provider type is empty"

	assert.Equal(t, expectedError, err.Error())
	assert.Nil(t, res)
}

func TestPromote_invalid_git_provider(t *testing.T) {
	tlsPublicKey, tlsPrivateKey, tlsCA := initTestTLS()

	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				Type: "subversion",
				URL:  "to-be-filled-in-by-test-code",
				SecretRef: meta.LocalObjectReference{
					Name: "repo-credentials",
				},
			},
		},
	}
	promotion := strategy.Promotion{
		PipelineNamespace: "foo",
		PipelineName:      "bar",
		Environment: v1alpha1.Environment{
			Name: "dev",
		},
	}
	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "foo",
				Name:      "repo-credentials",
			},
			Data: map[string][]byte{
				"token":    []byte("token"),
				"username": []byte("user"),
				"password": []byte("pass"),
				"caFile":   tlsCA,
			},
		},
	}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
			objects:       objects,
			gitServerOpts: &gitServerConfig{
				repoFixtureDir: "testdata/git/repository",
				username:       "user",
				password:       "pass",
				publicKey:      tlsPublicKey,
				privateKey:     tlsPrivateKey,
				ca:             tlsCA,
			},
		},
	)

	expectedError := "invalid git provider type: the Git provider \"subversion\" is not supported"

	assert.Equal(t, expectedError, err.Error())
	assert.Nil(t, res)
}

func TestPromote_github_happy_path_with_auth(t *testing.T) {
	tlsPublicKey, tlsPrivateKey, tlsCA := initTestTLS()

	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				Type:       "github",
				URL:        "to-be-filled-in-by-test-code",
				BaseBranch: "production",
				SecretRef: meta.LocalObjectReference{
					Name: "repo-credentials",
				},
			},
		},
	}
	promotion := strategy.Promotion{
		PipelineNamespace: "foo",
		PipelineName:      "bar",
		Version:           "1.2.3",
		Environment: v1alpha1.Environment{
			Name: "dev",
		},
	}
	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "foo",
				Name:      "repo-credentials",
			},
			Data: map[string][]byte{
				"token":    []byte("token"),
				"username": []byte("user"),
				"password": []byte("pass"),
				"caFile":   tlsCA,
			},
		},
	}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
			objects:       objects,
			gitServerOpts: &gitServerConfig{
				repoFixtureDir: "testdata/git/repository",
				username:       "user",
				password:       "pass",
				publicKey:      tlsPublicKey,
				privateKey:     tlsPrivateKey,
				ca:             tlsCA,
			},
			mockSetup: func(mockCtrl *gomock.Controller, promSpec v1alpha1.Promotion, promotion strategy.Promotion) (git.Provider, error) {
				repoRef, err := gitprovider.ParseOrgRepositoryURL(promSpec.Strategy.PullRequest.URL)
				if err != nil {
					return nil, err
				}
				repoRef.Domain = git.AddSchemeToDomain(repoRef.Domain)
				repoRef = git.WithCombinedSubOrgs(*repoRef)

				mockGitClient := NewMockClient(mockCtrl)
				mockOrgRepoClient := NewMockOrgRepositoriesClient(mockCtrl)
				mockOrgRepo := NewMockOrgRepository(mockCtrl)
				mockPRClient := NewMockPullRequestClient(mockCtrl)
				mockPR := NewMockPullRequest(mockCtrl)

				prDesc := fmt.Sprintf(`%s/%s/%s`, promotion.PipelineNamespace, promotion.PipelineName, promotion.Environment.Name)

				mockGitClient.EXPECT().OrgRepositories().Return(mockOrgRepoClient)
				mockOrgRepoClient.EXPECT().Get(gomock.Any(), gomock.Eq(*repoRef)).Return(mockOrgRepo, nil)
				mockOrgRepo.EXPECT().PullRequests().Return(mockPRClient)
				mockPR.EXPECT().Get().AnyTimes().Return(gitprovider.PullRequestInfo{})
				mockPRClient.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(promSpec.Strategy.PullRequest.BaseBranch), containsMatcher{x: prDesc}).Return(mockPR, nil)

				return git.NewFactory(logr.Discard()).Create(
					git.GitHubProviderName,
					git.WithConfiguredClient(mockGitClient),
				)
			},
		},
	)

	assert.NoError(t, err)
	assert.NotNil(t, res)
}

func TestPromote_bitbucket_happy_path_with_auth(t *testing.T) {
	tlsPublicKey, tlsPrivateKey, tlsCA := initTestTLS()

	promSpec := v1alpha1.Promotion{
		Strategy: v1alpha1.Strategy{
			PullRequest: &v1alpha1.PullRequestPromotion{
				Type: "github",
				URL:  "to-be-filled-in-by-test-code",
				SecretRef: meta.LocalObjectReference{
					Name: "repo-credentials",
				},
			},
		},
	}
	promotion := strategy.Promotion{
		PipelineNamespace: "foo",
		PipelineName:      "bar",
		Version:           "1.2.3",
		Environment: v1alpha1.Environment{
			Name: "dev",
		},
	}
	objects := []client.Object{
		&corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "foo",
				Name:      "repo-credentials",
			},
			Data: map[string][]byte{
				"token":    []byte("token"),
				"username": []byte("user"),
				"password": []byte("pass"),
				"caFile":   tlsCA,
			},
		},
	}

	res, err := newPromotionRequest(
		t,
		requestOptions{
			promotionSpec: promSpec,
			promotion:     promotion,
			objects:       objects,
			gitServerOpts: &gitServerConfig{
				repoFixtureDir: "testdata/git/repository",
				username:       "user",
				password:       "pass",
				publicKey:      tlsPublicKey,
				privateKey:     tlsPrivateKey,
				ca:             tlsCA,
			},
			mockSetup: func(mockCtrl *gomock.Controller, promSpec v1alpha1.Promotion, promotion strategy.Promotion) (git.Provider, error) {
				repoRef, err := git.GoGitProvider{}.ParseBitbucketServerURL(promSpec.Strategy.PullRequest.URL)
				if err != nil {
					return nil, err
				}

				mockGitClient := NewMockClient(mockCtrl)
				mockRepoClient := NewMockOrgRepositoriesClient(mockCtrl)
				mockRepo := NewMockOrgRepository(mockCtrl)
				mockPRClient := NewMockPullRequestClient(mockCtrl)
				mockRepoClient.EXPECT().Get(gomock.Any(), gomock.Eq(*repoRef)).Return(mockRepo, nil)
				mockGitClient.EXPECT().OrgRepositories().Return(mockRepoClient)
				mockRepo.EXPECT().PullRequests().Return(mockPRClient)
				mockPR := NewMockPullRequest(mockCtrl)
				mockPR.EXPECT().Get().AnyTimes().Return(gitprovider.PullRequestInfo{})
				prDesc := fmt.Sprintf(`%s/%s/%s`, promotion.PipelineNamespace, promotion.PipelineName, promotion.Environment.Name)

				mockPRClient.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(promSpec.Strategy.PullRequest.BaseBranch), containsMatcher{x: prDesc}).Return(mockPR, nil)

				return git.NewFactory(logr.Discard()).Create(
					git.BitBucketServerProviderName,
					git.WithConfiguredClient(mockGitClient),
				)
			},
		},
	)

	assert.NoError(t, err)
	assert.NotNil(t, res)
}

type requestOptions struct {
	promotionSpec v1alpha1.Promotion
	promotion     strategy.Promotion
	objects       []client.Object
	gitServerOpts *gitServerConfig
	mockSetup     func(*gomock.Controller, v1alpha1.Promotion, strategy.Promotion) (git.Provider, error)
}

func newPromotionRequest(t *testing.T, opts requestOptions) (*strategy.PromotionResult, error) {
	var (
		err       error
		server    *gittestserver.GitServer
		gitClient git.Provider
	)

	if opts.gitServerOpts != nil {
		server, opts.promotionSpec.Strategy.PullRequest.URL, err = newGitServer(opts.promotionSpec, *opts.gitServerOpts)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() {
			server.StopHTTP()
			os.RemoveAll(server.Root())
		})
	}

	fc := fake.NewClientBuilder().WithObjects(opts.objects...).Build()

	if opts.mockSetup != nil {
		mockCtrl := gomock.NewController(t)
		gitClient, err = opts.mockSetup(mockCtrl, opts.promotionSpec, opts.promotion)
		if err != nil {
			return nil, err
		}
	}

	mockCF := mockGitProviderFactory(gitClient)

	strat, err := pullrequest.New(fc, logger.NewLogger(logger.Options{}), pullrequest.GitClientFactory(mockCF))
	if err != nil {
		return nil, err
	}

	return strat.Promote(context.Background(), opts.promotionSpec, opts.promotion)
}

func newGitServer(promSpec v1alpha1.Promotion, opts gitServerConfig) (*gittestserver.GitServer, string, error) {
	server, err := gittestserver.NewTempGitServer()
	if err != nil {
		return server, "", err
	}

	server.AutoCreate()
	repoPath := "/org/test.git"

	repoName := promSpec.Strategy.PullRequest.BaseBranch
	if repoName == "" {
		repoName = "main"
	}

	_, err = initGitRepo(server, opts.repoFixtureDir, repoName, repoPath)
	if err != nil {
		return server, "", err
	}

	if opts.username != "" {
		server.Auth(opts.username, opts.password)
	}

	if opts.privateKey != nil {
		if err = server.StartHTTPS(opts.publicKey, opts.privateKey, opts.ca, "example.org"); err != nil {
			return server, "", err
		}
	} else {
		if err := server.StartHTTP(); err != nil {
			return server, "", err
		}
	}

	return server, server.HTTPAddress() + repoPath, nil
}

var _ gomock.Matcher = containsMatcher{}

type containsMatcher struct {
	x string
}

func (m containsMatcher) Matches(x interface{}) bool {
	return strings.Contains(x.(string), m.x)
}

func (m containsMatcher) String() string {
	return m.x
}
