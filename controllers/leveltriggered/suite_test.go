package leveltriggered

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"

	// These aren't needed by the controller, but are useful to have
	// for creating target objects in test code
	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	kustomv1 "github.com/fluxcd/kustomize-controller/api/v1"

	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

var k8sManager ctrl.Manager
var k8sClient client.Client
var testEnv *envtest.Environment
var kubeConfig []byte
var eventRecorder *testEventRecorder
var pipelineReconciler *PipelineReconciler

var envsToStop []*envtest.Environment

type testEvent struct {
	object    runtime.Object
	eventType string
	reason    string
	message   string
}

type testEventRecorder struct {
	events map[string][]testEvent
}

func (t *testEventRecorder) Events(name string) []testEvent {
	return t.events[name]
}

func (t *testEventRecorder) Reset() {
	t.events = map[string][]testEvent{}
}

func (t *testEventRecorder) Event(object runtime.Object, eventtype string, reason string, message string) {
	key := ""
	if objMeta, err := meta.Accessor(object); err == nil {
		key = fmt.Sprintf("%s/%s", objMeta.GetNamespace(), objMeta.GetName())
	}
	t.events[key] = append(t.events[key], testEvent{
		object:    object,
		eventType: eventtype,
		reason:    reason,
		message:   message,
	})
}

// Eventf is just like Event, but with Sprintf for the message field.
func (t *testEventRecorder) Eventf(object runtime.Object, eventtype string, reason string, messageFmt string, args ...interface{}) {
	key := ""
	if objMeta, err := meta.Accessor(object); err == nil {
		key = fmt.Sprintf("%s/%s", objMeta.GetNamespace(), objMeta.GetName())
	}
	t.events[key] = append(t.events[key], testEvent{
		object:    object,
		eventType: eventtype,
		reason:    reason,
		message:   fmt.Sprintf(messageFmt, args...),
	})
}

// AnnotatedEventf is just like eventf, but with annotations attached
func (t *testEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype string, reason string, messageFmt string, args ...interface{}) {
	// unused, leaving it with panic so when someone tries to use it, they will be
	// reminded to implement.
	panic("not implemented")
}

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("..", "..", "config", "testdata", "crds"),
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		log.Fatalf("starting test env failed: %s", err)
	}
	envsToStop = append(envsToStop, testEnv)

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

	err = v1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		log.Fatalf("add pipelines to schema failed: %s", err)
	}
	err = clusterctrlv1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		log.Fatalf("add GitopsCluster to schema failed: %s", err)
	}

	err = helmv2.AddToScheme(scheme.Scheme)
	if err != nil {
		log.Fatalf("add HelmRelease to schema failed: %s", err)
	}
	err = kustomv1.AddToScheme(scheme.Scheme)
	if err != nil {
		log.Fatalf("add Kustomization to schema failed: %s", err)
	}

	ctrl.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: ":18080",
	})
	if err != nil {
		log.Fatalf("initializing controller manager failed: %s", err)
	}

	eventRecorder = &testEventRecorder{events: map[string][]testEvent{}}

	pipelineReconciler = NewPipelineReconciler(
		k8sManager.GetClient(),
		scheme.Scheme,
		"pipelines",
		eventRecorder,
		strategy.StrategyRegistry{},
	)
	err = pipelineReconciler.SetupWithManager(k8sManager)
	if err != nil {
		log.Fatalf("setup pipeline controller failed: %s", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		err = k8sManager.Start(ctx)
		if err != nil {
			log.Fatalf("starting controller manager failed: %s", err)
		}
		wg.Done()
	}()

	k8sClient = k8sManager.GetClient()
	if k8sClient == nil {
		log.Fatalf("failed getting k8s client: k8sManager.GetClient() returned nil")
	}

	retCode := m.Run()

	cancel()
	wg.Wait()
	log.Println("manager exited")

	var failedToStopEnvs bool
	for _, env := range envsToStop {
		err = env.Stop()
		if err != nil {
			failedToStopEnvs = true
			log.Printf("stopping test env failed: %s\n", err)
		}
	}
	if failedToStopEnvs {
		log.Fatalf("failed to stop all test envs")
	}

	log.Println("test envs stopped")
	os.Exit(retCode)
}
