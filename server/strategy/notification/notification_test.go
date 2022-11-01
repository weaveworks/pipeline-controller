package notification_test

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/internal/testingutils"
	"github.com/weaveworks/pipeline-controller/server/strategy"
	"github.com/weaveworks/pipeline-controller/server/strategy/notification"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
			"nil NotificationPromotion",
			v1alpha1.Promotion{Notification: nil},
			false,
		},
		{
			"empty NotificationPromotion",
			v1alpha1.Promotion{Notification: &v1alpha1.NotificationPromotion{}},
			true,
		},
	}

	strat, err := notification.NewNotification(nil, nil)
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

func TestPromote(t *testing.T) {
	g := testingutils.NewGomegaWithT(t)
	name := "test-pipeline"
	ns := "test-ns"

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}

	err := v1alpha1.AddToScheme(scheme.Scheme)
	g.Expect(err).NotTo(HaveOccurred())

	fc := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects([]client.Object{pipeline}...).Build()
	eventRecorder := record.NewFakeRecorder(5)

	strat, err := notification.NewNotification(fc, eventRecorder)
	g.Expect(err).NotTo(HaveOccurred())

	promSpec := v1alpha1.Promotion{
		Notification: &v1alpha1.NotificationPromotion{},
	}

	promotion := strategy.Promotion{
		PipelineName:      name,
		PipelineNamespace: ns,
		Environment:       v1alpha1.Environment{Name: "test"},
		Version:           "v0.1.2",
	}

	_, err = strat.Promote(context.Background(), promSpec, promotion)
	g.Expect(err).NotTo(HaveOccurred())

	g.Eventually(eventRecorder.Events).Should(Receive(Equal("Normal Promote Promote pipeline test-ns/test-pipeline to test with version v0.1.2")))
}
