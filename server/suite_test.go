package server_test

import (
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/fluxcd/helm-controller/api/v2beta1"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

var k8sClient client.Client
var testEnv *envtest.Environment

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

	k8sClient, err = client.New(cfg, client.Options{})
	if err != nil {
		log.Fatalf("creating client failed: %s", err)
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

	retCode := m.Run()

	err = testEnv.Stop()
	if err != nil {
		log.Fatalf("stoping test env failed: %s", err)
	}

	os.Exit(retCode)
}
