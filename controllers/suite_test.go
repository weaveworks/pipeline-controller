package controllers

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/fluxcd/helm-controller/api/v2beta1"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

var k8sManager ctrl.Manager
var k8sClient client.Client
var testEnv *envtest.Environment
var kubeConfig []byte
var eventRecorder *testEventRecorder

type testEvent struct {
	object    runtime.Object
	eventType string
	reason    string
	message   string
}

type testEventRecorder struct {
	events []testEvent
}

func (t *testEventRecorder) Events() []testEvent {
	return t.events
}

func (t *testEventRecorder) Reset() {
	t.events = []testEvent{}
}

func (t *testEventRecorder) Event(object runtime.Object, eventtype string, reason string, message string) {
	t.events = append(t.events, testEvent{
		object:    object,
		eventType: eventtype,
		reason:    reason,
		message:   message,
	})
}

// Eventf is just like Event, but with Sprintf for the message field.
func (t *testEventRecorder) Eventf(object runtime.Object, eventtype string, reason string, messageFmt string, args ...interface{}) {
	t.events = append(t.events, testEvent{
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

	eventRecorder = &testEventRecorder{events: []testEvent{}}

	err = (&PipelineReconciler{
		Client:       k8sManager.GetClient(),
		Scheme:       scheme.Scheme,
		targetScheme: scheme.Scheme,
		recorder:     eventRecorder,
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
