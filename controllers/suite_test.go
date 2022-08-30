package controllers

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/fluxcd/helm-controller/api/v2beta1"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var k8sManager ctrl.Manager
var k8sClient client.Client
var testEnv *envtest.Environment
var kubeConfig []byte

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "config", "crd", "bases"),
			filepath.Join("..", "config", "testdata", "crds"),
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		log.Fatalf("starting test env failed: %s", err)
	}

	user, err := testEnv.ControlPlane.AddUser(envtest.User{
		Name:   "envtest-admin",
		Groups: []string{"system:masters"},
	}, nil)
	if err != nil {
		log.Fatalf("add user failed: %s", err)
	}

	kubeConfig, err = user.KubeConfig()
	if err != nil {
		log.Fatalf("get user kubeconfig failed: %s", err)
	}

	err = v2beta1.AddToScheme(scheme.Scheme)
	if err != nil {
		log.Fatalf("add helm to schema failed: %s", err)
	}

	err = v1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		log.Fatalf("add pipelines to schema failed: %s", err)
	}
	err = clusterctrlv1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		log.Fatalf("add GitopsCluster to schema failed: %s", err)
	}

	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		log.Fatalf("initializing controller manager failed: %s", err)
	}

	err = (&PipelineReconciler{
		Client:       k8sManager.GetClient(),
		Scheme:       scheme.Scheme,
		targetScheme: scheme.Scheme,
	}).SetupWithManager(k8sManager)
	if err != nil {
		log.Fatalf("setup pipeline controller failed: %s", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		err = k8sManager.Start(ctx)
		if err != nil {
			log.Fatalf("starting controller manager failed: %s", err)
		}
	}()

	k8sClient = k8sManager.GetClient()
	if k8sClient == nil {
		log.Fatalf("failed getting k8s client: k8sManager.GetClient() returned nil")
	}

	retCode := m.Run()

	cancel()

	err = testEnv.Stop()
	if err != nil {
		log.Fatalf("stoping test env failed: %s", err)
	}

	os.Exit(retCode)
}
