package watcher

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/go-logr/logr"
	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/controllers"
	"github.com/weaveworks/pipeline-controller/pkg/gitopscluster"
	"github.com/weaveworks/pipeline-controller/server/strategy"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type watcher struct {
	log            logr.Logger
	k8sClient      client.Client
	stratReg       strategy.StrategyRegistry
	clientsManager gitopscluster.RestConfigManager

	informersMu sync.Mutex
	informers   map[string]context.CancelFunc
}

func New(log logr.Logger, k8sClient client.Client, stratReg strategy.StrategyRegistry, clientsManager gitopscluster.RestConfigManager) *watcher {
	return &watcher{
		log:            log,
		k8sClient:      k8sClient,
		stratReg:       stratReg,
		clientsManager: clientsManager,
		informers:      make(map[string]context.CancelFunc),
	}
}

func (w *watcher) Start() error {
	w.clientsManager.AddEventHandler(gitopscluster.EventHandlerFuncs{
		AddFunc: func(name string, namespace string, restConfig *rest.Config) {
			w.log.Info("starting informer", "name", name, "namespace", namespace)
			go w.runInformer(name, namespace, restConfig)
		},

		DeleteFunc: func(name string, namespace string) {
			w.log.Info("deleting informer", "name", name, "namespace", namespace)

			cancel, ok := w.informers[name]
			if !ok {
				w.log.Error(nil, "informer not found", "name", name, "namespace", namespace)
				return
			}

			cancel()

			w.informersMu.Lock()
			delete(w.informers, name)
			w.informersMu.Unlock()
		},
	})

	return nil
}

func (w *watcher) runInformer(name string, namespace string, restConfig *rest.Config) {
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		w.log.Error(err, "failed to create dynamic client", "name", name, "namespace", namespace)
		return
	}

	resource := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2beta1", Resource: "helmreleases"}
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynamicClient, time.Minute, corev1.NamespaceAll, nil)
	informer := factory.ForResource(resource).Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			applied, err := hasAppliedNewRevision(w.log, name, namespace, oldObj, newObj)
			if err != nil {
				w.log.Error(err, "failed to check if applied revision changed", "name", name, "namespace", namespace)
				return
			}

			if !applied {
				return
			}

			release, err := interfaceToHelmRelease(newObj)
			if err != nil {
				w.log.Error(err, "failed to convert unstructured to helmrelease", "name", name, "namespace", namespace)
				return
			}

			labels := release.GetLabels()
			pipelineName := labels[controllers.PipelineLabelKeyName]
			pipelineNamespace := labels[controllers.PipelineLabelKeyNamespace]
			pipelineEnv := labels[controllers.PipelineLabelKeyEnv]

			ctx := context.Background()
			var pipeline v1alpha1.Pipeline
			if err := w.k8sClient.Get(ctx, client.ObjectKey{Namespace: pipelineNamespace, Name: pipelineName}, &pipeline); err != nil {
				w.log.Error(err, "could not fetch Pipeline object", "name", pipelineName, "namespace", pipelineNamespace)
				return
			}

			nextEnv, err := getNextEnvironment(pipeline, pipelineEnv)
			if err != nil {
				w.log.Error(err, "could not get next environment", "name", pipelineName, "namespace", pipelineNamespace)
				return
			}

			if nextEnv == nil {
				w.log.Info("last environment", "name", pipelineName, "namespace", pipelineNamespace, "env", pipelineEnv)
				return
			}

			if len(nextEnv.Targets) == 0 {
				w.log.Error(err, "environment has no targets", "name", pipelineName, "namespace", pipelineNamespace, "env", pipelineEnv)
				return
			}

			promSpec := pipeline.Spec.GetPromotion(nextEnv.Name)
			if promSpec == nil {
				w.log.Error(err, "no promotion configured in Pipeline resource", "name", pipelineName, "namespace", pipelineNamespace, "env", pipelineEnv)
				return
			}

			strat, err := w.stratReg.Get(*promSpec)
			if err != nil {
				w.log.Error(err, "error getting strategy from registry", "name", pipelineName, "namespace", pipelineNamespace, "env", pipelineEnv)
				return
			}

			promotion := strategy.Promotion{
				PipelineNamespace: pipelineNamespace,
				PipelineName:      pipelineName,
				Environment:       *nextEnv,
				Version:           release.Spec.Chart.Spec.Version,
			}

			res, err := strat.Promote(ctx, *promSpec, promotion)
			if err != nil {
				w.log.Error(err, "error promoting", "name", pipelineName, "namespace", pipelineNamespace, "env", pipelineEnv)
				return
			}

			w.log.Info("promoted", "name", pipelineName, "namespace", pipelineNamespace, "env", pipelineEnv, "location", res.Location)
		},
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	w.informersMu.Lock()
	w.informers[name] = cancel
	w.informersMu.Unlock()

	informer.Run(ctx.Done())
}

func hasAppliedNewRevision(logger logr.Logger, name, namespace string, oldObj, newObj interface{}) (bool, error) {
	oldRelease, err := interfaceToHelmRelease(oldObj)
	if err != nil {
		return false, fmt.Errorf("failed to convert unstructured to helmrelease: %w", err)
	}

	newRelease, err := interfaceToHelmRelease(newObj)
	if err != nil {
		return false, fmt.Errorf("failed to convert unstructured to helmrelease: %w", err)
	}

	return newRelease.Status.LastAppliedRevision != oldRelease.Status.LastAppliedRevision, nil
}

func interfaceToHelmRelease(obj interface{}) (*helmv2.HelmRelease, error) {
	u := obj.(*unstructured.Unstructured)
	release := &helmv2.HelmRelease{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.UnstructuredContent(), release)
	if err != nil {
		return nil, fmt.Errorf("failed to convert unstructured to helmrelease: %w", err)
	}

	return release, nil
}

func getNextEnvironment(pipeline v1alpha1.Pipeline, curEnv string) (*v1alpha1.Environment, error) {
	for idx, pEnv := range pipeline.Spec.Environments {
		if pEnv.Name == curEnv {
			if idx == len(pipeline.Spec.Environments)-1 {
				return nil, nil
			}

			return &pipeline.Spec.Environments[idx+1], nil
		}
	}

	return nil, fmt.Errorf("environment %s not found in pipeline %s", curEnv, pipeline.Name)
}
