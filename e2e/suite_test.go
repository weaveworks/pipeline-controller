//go:build e2e

package e2e

import (
	"context"
	"github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/onsi/gomega"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"log"
	"os"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"testing"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

var k8sClient client.Client
var clientset *kubernetes.Clientset

func TestMain(m *testing.M) {
	var err error

	useExistingCluster := true
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "config", "testdata", "crds"),
		},

		ErrorIfCRDPathMissing: true,
		UseExistingCluster:    &useExistingCluster,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		log.Fatalf("starting test env failed: %s", err)
	}
	log.Println("environment started")
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
	_, cancel := context.WithCancel(context.Background())

	k8sClient, err = client.New(cfg, client.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		log.Fatalf("cannot create kubernetes client: %s", err)
	}

	clientset, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("cannot create kubernetes client: %s", err)
	}
	log.Println("kube client created")

	gomega.RegisterFailHandler(func(message string, skip ...int) {
		log.Println(message)
	})

	retCode := m.Run()
	log.Printf("suite ran with return code: %d", retCode)

	cancel()

	err = testEnv.Stop()
	if err != nil {
		log.Fatalf("stoping test env failed: %s", err)
	}

	log.Println("test environment stopped")
	os.Exit(retCode)
}
