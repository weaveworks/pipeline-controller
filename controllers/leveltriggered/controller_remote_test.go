package leveltriggered

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fluxcd/pkg/apis/meta"
	. "github.com/onsi/gomega"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Test that pipelines can target application objects that live in remote clusters.
func TestRemoteTargets(t *testing.T) {
	// Our remote cluster(s) will be testenv(s)
	leafEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("..", "..", "config", "testdata", "crds"),
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := leafEnv.Start()
	if err != nil {
		t.Error("starting leaf test env failed", err)
	}

	user, err := leafEnv.ControlPlane.AddUser(envtest.User{
		Name:   "leaf-admin",
		Groups: []string{"system:masters"},
	}, nil)
	if err != nil {
		t.Error("add user failed", err)
	}

	kubeConfig, err := user.KubeConfig()
	if err != nil {
		t.Errorf("get user kubeconfig failed: %s", err)
	}

	kubeconfigSecretName := rand.String(5)
	secret := &corev1.Secret{
		StringData: map[string]string{
			"kubeconfig": string(kubeConfig),
		},
	}
	secret.Name = kubeconfigSecretName
	secret.Namespace = "default"
	if err = k8sClient.Create(context.TODO(), secret); err != nil {
		t.Error("could not create kubeconfig secret", err)
	}

	leafClient, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Error("failed to create client for leaf test env", err)
	}

	// Create a GitOpsCluster object representing the leaf test env
	leafCluster := &clusterctrlv1alpha1.GitopsCluster{
		Spec: clusterctrlv1alpha1.GitopsClusterSpec{
			SecretRef: &meta.LocalObjectReference{
				Name: kubeconfigSecretName,
			},
		},
	}
	leafCluster.Name = "leaf1"
	leafCluster.Namespace = "default"
	if err = k8sClient.Create(context.TODO(), leafCluster); err != nil {
		t.Error("could not create leaf cluster GitopsCluster", err)
	}

	t.Run("sets reconciliation succeeded condition for remote cluster", func(t *testing.T) {
		g := testingutils.NewGomegaWithT(t)
		ctx := context.TODO()

		name := "pipeline-" + rand.String(5)
		ns := testingutils.NewNamespace(ctx, g, k8sClient)

		// for reconciliation to succeed, the cluster has to be 1. ready and 2. reachable, and the application has to be present.
		apimeta.SetStatusCondition(&leafCluster.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "test"})
		g.Expect(k8sClient.Status().Update(ctx, leafCluster)).To(Succeed())

		pipeline := newPipeline(name, ns.Name, []*clusterctrlv1alpha1.GitopsCluster{leafCluster})
		g.Expect(k8sClient.Create(ctx, pipeline)).To(Succeed())

		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionFalse, v1alpha1.TargetNotReadableReason)

		// the application hasn't been created, so we expect "not found"
		p := getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
		g.Expect(getTargetStatus(g, p, "test", 0).Ready).NotTo(BeTrue())
		// we can see "target cluster client not synced" before "not found"
		g.Eventually(func() string {
			return getTargetStatus(g, p, "test", 0).Error
		}).Should(ContainSubstring("not found"))

		targetNs := corev1.Namespace{}
		targetNs.Name = ns.Name // newPipeline makes the target namespace the same as the pipeline namespace
		g.Expect(leafClient.Create(ctx, &targetNs)).To(Succeed())
		hr := createApp(ctx, leafClient, g, name, targetNs.Name) // the name of the pipeline is also used as the name in the appRef, in newPipeline(...)

		// now it's there, the pipeline can be reconciled fully
		checkCondition(ctx, g, client.ObjectKeyFromObject(pipeline), meta.ReadyCondition, metav1.ConditionTrue, v1alpha1.ReconciliationSucceededReason)
		// .. and the target status will be unready, but no error
		p = getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
		g.Expect(getTargetStatus(g, p, "test", 0).Ready).NotTo(BeTrue())
		g.Expect(getTargetStatus(g, p, "test", 0).Error).To(BeEmpty())

		// make the app ready, and check it's recorded as such in the pipeline status
		const appRevision = "v1.0.1"
		hr.Status.LastAppliedRevision = appRevision
		apimeta.SetStatusCondition(&hr.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "test"})
		g.Expect(leafClient.Status().Update(ctx, hr)).To(Succeed())

		var targetStatus v1alpha1.TargetStatus
		g.Eventually(func() bool {
			p := getPipeline(ctx, g, client.ObjectKeyFromObject(pipeline))
			targetStatus = getTargetStatus(g, p, "test", 0)
			return targetStatus.Ready
		}, "5s", "0.2s").Should(BeTrue())
		g.Expect(targetStatus.Revision).To(Equal(appRevision))

		// TODO possibly: check the events too, as it used to.
	})
}
