//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/fluxcd/go-git-providers/gitprovider"
	"github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/pkg/runtime/logger"
	"github.com/go-logr/logr"
	"github.com/google/go-github/v49/github"
	"github.com/hashicorp/go-uuid"
	. "github.com/onsi/gomega"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"github.com/weaveworks/pipeline-controller/pkg/conditions"
	"github.com/weaveworks/pipeline-controller/server/strategy/pullrequest"
	gitlab2 "github.com/xanzy/go-gitlab"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultTimeout  = time.Second * 30
	defaultInterval = time.Second
	defaultSince    = 5 * defaultInterval / time.Second
)

func TestPullRequestPromotions(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	log := logger.NewLogger(logger.Options{})

	tests := []struct {
		name              string
		pipelineName      string
		pipelineNamespace string
		branchName        string
		environment       string
		currentVersion    string
		newVersion        string
	}{
		{
			"can promote github",
			"podinfo-github",
			"flux-system",
			"promotion-flux-system-podinfo-github-prod",
			"prod",
			"6.0.0",
			"6.0.1",
		},
		{
			"can promote gitlab",
			"podinfo-gitlab",
			"flux-system",
			"promotion-flux-system-podinfo-gitlab-prod",
			"prod",
			"6.0.0",
			"6.0.1",
		},
	}

	g.SetDefaultEventuallyTimeout(defaultTimeout)
	g.SetDefaultEventuallyPollingInterval(defaultInterval)

	for _, promotion := range tests {
		t.Run(promotion.name, func(t *testing.T) {

			//Given a pipeline is ready to promote
			pipeline, err := ensurePipeline(g, k8sClient, promotion.pipelineNamespace, promotion.pipelineName)
			g.Expect(err).To(BeNil())
			helmRelease, err := ensureHelmRelease(g, pipeline, k8sClient, promotion.currentVersion)
			g.Expect(err).To(BeNil())

			//cleanup promotion
			t.Cleanup(func() {
				log.Info("cleaning test")
				//restore helm release version
				released := releaseNewVersion(g, k8sClient, helmRelease, promotion.currentVersion)
				g.Expect(released).To(BeTrue())
				helmRelease, err = ensureHelmRelease(g, pipeline, k8sClient, promotion.currentVersion)
				//delete git branch
				err := deleteGitBranchByName(context.Background(), g, k8sClient, pipeline, promotion.branchName, log)
				if err != nil {
					log.Error(err, "could not delete branch")
				}
			})

			//When promoted
			released := releaseNewVersion(g, k8sClient, helmRelease, promotion.newVersion)
			g.Expect(released).To(BeTrue())
			helmRelease, err = ensureHelmRelease(g, pipeline, k8sClient, promotion.newVersion)
			g.Expect(err).To(BeNil())

			//Then pull request has been created
			assertPullRequestCreated(g, clientset, pipeline, promotion.environment, promotion.newVersion)
		})
	}

	//after test
	deletePodsByNamespaceAndLabel(g, "flux-system", "app=helm-controller")
	deletePodsByNamespaceAndLabel(g, "flux-system", "app=notification-controller")
}

func deleteGitBranchByName(ctx context.Context, g *WithT, c client.Client, pipeline v1alpha1.Pipeline, branchName string, log logr.Logger) error {
	var secret corev1.Secret

	promotion := pipeline.Spec.Promotion
	if promotion == nil {
		return fmt.Errorf("cannot delete branch for pipeline without promotion")
	}

	pullRequestPromotion := promotion.Strategy.PullRequest
	if pullRequestPromotion == nil {
		return fmt.Errorf("cannot delete branch for pipelines without pullRequest")
	}

	secretName := pullRequestPromotion.SecretRef.Name
	userRepoRef, err := gitprovider.ParseUserRepositoryURL(pullRequestPromotion.URL)
	if err != nil {
		return fmt.Errorf("could not parse git url: %w", err)
	}
	g.Eventually(func() bool {
		//get secret
		if err := c.Get(ctx, client.ObjectKey{Namespace: pipeline.Namespace, Name: secretName}, &secret); err != nil {
			log.Error(err, "failed to fetch Secret")
			return false
		}
		return true
	}).Should(BeTrue())

	tokenString := string(secret.Data["token"])
	provider := pullrequest.GitProviderConfig{
		Token:            tokenString,
		TokenType:        "oauth2",
		Type:             pullRequestPromotion.Type,
		Domain:           userRepoRef.Domain,
		DestructiveCalls: false,
	}

	gitProviderClient, err := pullrequest.NewGitProviderClientFactory(log)(provider)
	if err != nil {
		return fmt.Errorf("could not create git provider client: %w", err)
	}
	//TODO it should not be needed - gitlab ggp issue
	userRepoRef.Domain = gitProviderClient.SupportedDomain()
	//TODO contribute me to ggp
	switch pullRequestPromotion.Type {
	case v1alpha1.Github:
		//cannot delete github branch so renaming
		clientRaw := gitProviderClient.RawClient().(gitprovider.Client).Raw().(*github.Client)
		generatedUuid, err := uuid.GenerateUUID()
		if err != nil {
			return fmt.Errorf("could not generate uuid: %w", err)
		}
		owner := userRepoRef.UserLogin
		repo := userRepoRef.RepositoryName
		_, _, err = clientRaw.Repositories.RenameBranch(ctx, owner, repo, branchName, fmt.Sprintf("%s-%s", branchName, generatedUuid))
		if err != nil {
			return fmt.Errorf("could not rename branch: %w", err)
		}
	case v1alpha1.Gitlab:
		clientRaw := gitProviderClient.RawClient().(gitprovider.Client).Raw().(*gitlab2.Client)
		glRepo, err := gitProviderClient.RawClient().(gitprovider.Client).UserRepositories().Get(ctx, *userRepoRef)
		if err != nil {
			return fmt.Errorf("could not delete branch: %w", err)
		}
		gitlabProject := glRepo.APIObject().(*gitlab2.Project)
		_, err = clientRaw.Branches.DeleteBranch(gitlabProject.ID, branchName, nil)
		if err != nil {
			return fmt.Errorf("could not delete gitlab branch: %w", err)
		}
	}
	return nil
}

type Promotion struct {
	PipelineNamespace string      `json:"pipelineNamespace,omitempty"`
	PipelineName      string      `json:"pipelineName,omitempty"`
	Environment       Environment `json:"environment,omitempty"`
	Version           string      `json:"version,omitempty"`
}
type Environment struct {
	Name string `json:"name,omitempty"`
}
type PromotionLogEvent struct {
	Msg       string    `json:"msg,omitempty"`
	Strategy  string    `json:"strategy,omitempty"`
	Promotion Promotion `json:"promotion,omitempty"`
}

// Asserts creation of pull request by checking controller logs.
// TODO this approach has limitations and we should change it for checking some stored state out of the creation
// of the pull request or similar
func assertPullRequestCreated(g *WithT, clientset *kubernetes.Clientset, pipeline v1alpha1.Pipeline, environment string, version string) {
	log.Println("find pull request")
	ctx := context.Background()
	g.Eventually(func() bool {
		var promotionLog PromotionLogEvent

		listOptions := metav1.ListOptions{
			LabelSelector: "app=pipeline-controller",
		}
		pods, err := clientset.CoreV1().Pods("pipeline-system").List(ctx, listOptions)
		if err != nil {
			log.Printf("could not get pipeline controller pod: %s", err)
			return false
		}
		pipelineControllerPod := pods.Items[0]
		sinceSeconds := int64(defaultSince)
		podLogOpts := corev1.PodLogOptions{
			SinceSeconds: &sinceSeconds,
		}
		req := clientset.CoreV1().Pods(pipelineControllerPod.Namespace).GetLogs(pipelineControllerPod.Name, &podLogOpts)
		podLogs, err := req.Stream(ctx)
		if err != nil {
			log.Printf("could not get pods logs: %s", err)
			return false
		}

		defer func(podLogs io.ReadCloser) {
			err := podLogs.Close()
			if err != nil {
				log.Printf("error closing pod logs: %s", err.Error())
			}
		}(podLogs)
		buf := new(bytes.Buffer)
		_, err = io.Copy(buf, podLogs)
		if err != nil {
			log.Printf("could not get pods logs: %s", err)
			return false
		}
		// TODO change me when we have some state to check on the promotion or similar
		logsAsString := buf.String()
		log.Printf("pipeline controller logs %s", logsAsString)
		for _, line := range strings.Split(strings.TrimSuffix(logsAsString, "\n"), "\n") {
			if !strings.Contains(line, "created PR") {
				continue
			}
			_ = json.Unmarshal([]byte(line), &promotionLog)
			foundPipelineName := promotionLog.Promotion.PipelineName == pipeline.Name
			foundPipelineNs := promotionLog.Promotion.PipelineNamespace == pipeline.Namespace
			foundEnvironment := promotionLog.Promotion.Environment.Name == environment
			foundVersion := promotionLog.Promotion.Version == version
			if foundPipelineName && foundPipelineNs && foundVersion && foundEnvironment {
				log.Println("found pull request")
				return true
			}
		}
		return false
	}).Should(BeTrue())
}

func releaseNewVersion(g *WithT, c client.Client, devHelmRelease v2beta1.HelmRelease, newVersion string) bool {
	log.Println("patching helm release")
	ctx := context.Background()
	// Release by patching a helm release like
	// kubectl patch helmreleases.helm.toolkit.fluxcd.io -n dev podinfo -p '{"spec":{"chart":{"spec": {"version": "6.0.0"}}}}' --type=merge
	return g.Eventually(func() bool {
		patchString := fmt.Sprintf("{\"spec\":{\"chart\":{\"spec\": {\"version\": \"%s\"}}}}", newVersion)
		patch := []byte(patchString)
		var err error
		if err = c.Patch(ctx, &devHelmRelease, client.RawPatch(types.MergePatchType, patch)); err != nil {
			log.Printf("could not patch release: %s", err)
			return false
		}
		log.Println("helm release patched")
		return true
	}).Should(BeTrue())
}

func ensureHelmRelease(g *WithT, pipeline v1alpha1.Pipeline, c client.Client, currentVersion string) (v2beta1.HelmRelease, error) {
	log.Println("find helm release")
	var helmRelease v2beta1.HelmRelease
	ctx := context.Background()
	// AND helm release to promote in dev and prod
	helmReleaseFound := g.Eventually(func() bool {
		devEnvironmentNs := pipeline.Spec.Environments[0].Targets[0].Namespace
		devEnvironmentAppName := pipeline.Spec.AppRef.Name
		if err := c.Get(ctx, client.ObjectKey{Namespace: devEnvironmentNs, Name: devEnvironmentAppName}, &helmRelease); err != nil {
			log.Printf("could not find helm relese: %s", err)
			return false
		}
		expectedVersion := helmRelease.Status.LastAppliedRevision == currentVersion
		return expectedVersion && conditions.IsReady(helmRelease.Status.Conditions)
	}).Should(BeTrue())
	if !helmReleaseFound {
		return helmRelease, fmt.Errorf("helm release not found")
	}
	log.Println("helm release found")
	return helmRelease, nil
}

func ensurePipeline(g *WithT, c client.Client, pipelineNamespace string, pipelineName string) (v1alpha1.Pipeline, error) {
	log.Println("find pipeline")
	var pipeline v1alpha1.Pipeline
	ctx := context.Background()
	foundPipeline := g.Eventually(func() bool {
		if err := c.Get(ctx, client.ObjectKey{Namespace: pipelineNamespace, Name: pipelineName}, &pipeline); err != nil {
			log.Printf("could not find pipeline: %s", err)
			return false
		}
		return conditions.IsReady(pipeline.Status.Conditions)
	}).Should(BeTrue())
	if !foundPipeline {
		return pipeline, fmt.Errorf("pipeline not found")
	}
	log.Println("pipeline found")
	return pipeline, nil
}

func deletePodsByNamespaceAndLabel(g *WithT, namespace string, labelSelector string) bool {
	return g.Eventually(func() bool {
		ctx := context.Background()
		listOptions := metav1.ListOptions{
			LabelSelector: labelSelector,
		}
		deleteOptions := metav1.DeleteOptions{}
		if err := clientset.CoreV1().Pods(namespace).DeleteCollection(ctx, deleteOptions, listOptions); err != nil {
			log.Printf("coudl not delete pods: %s", err)
			return false
		}
		log.Printf("pod deleted: %s", labelSelector)
		return true
	}).Should(BeTrue())
}
