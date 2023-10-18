package leveltriggered

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	capicfg "sigs.k8s.io/cluster-api/util/kubeconfig"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/pkg/conditions"
	"github.com/weaveworks/pipeline-controller/server/strategy"
)

// clusterAndGVK is used as the key for caches
type clusterAndGVK struct {
	client.ObjectKey
	schema.GroupVersionKind
}

func (key clusterAndGVK) String() string {
	return key.ObjectKey.String() + ":" + key.GroupVersionKind.String()
}

type cacheAndCancel struct {
	cache  cache.Cache
	cancel context.CancelFunc
}

// PipelineReconciler reconciles a Pipeline object
type PipelineReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	targetScheme   *runtime.Scheme
	ControllerName string
	recorder       record.EventRecorder
	stratReg       strategy.StrategyRegistry

	caches    map[clusterAndGVK]cacheAndCancel
	cachesMu  *sync.Mutex
	cachesGC  *gc
	runner    *runner
	manager   ctrl.Manager
	appEvents chan event.GenericEvent
}

func NewPipelineReconciler(c client.Client, s *runtime.Scheme, controllerName string, eventRecorder record.EventRecorder, stratReg strategy.StrategyRegistry) *PipelineReconciler {
	targetScheme := runtime.NewScheme()

	pc := &PipelineReconciler{
		Client:         c,
		Scheme:         s,
		targetScheme:   targetScheme,
		recorder:       eventRecorder,
		ControllerName: controllerName,
		stratReg:       stratReg,
		caches:         make(map[clusterAndGVK]cacheAndCancel),
		cachesMu:       &sync.Mutex{},
		appEvents:      make(chan event.GenericEvent),
	}
	return pc
}

func (r *PipelineReconciler) getCacheKeys() (res []clusterAndGVK) {
	r.cachesMu.Lock()
	defer r.cachesMu.Unlock()
	for k := range r.caches {
		res = append(res, k)
	}
	return res
}

//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pipelines.weave.works,resources=pipelines/finalizers,verbs=update
//+kubebuilder:rbac:groups=gitops.weave.works,resources=gitopsclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

func (r *PipelineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("starting reconciliation")

	var pipeline v1alpha1.Pipeline
	if err := r.Get(ctx, req.NamespacedName, &pipeline); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Examine if the object is under deletion
	if !pipeline.ObjectMeta.DeletionTimestamp.IsZero() {
		// Stop reconciliation as the object is being deleted
		return ctrl.Result{}, nil
	}

	envStatuses := map[string]*v1alpha1.EnvironmentStatus{}
	var unready bool

	for _, env := range pipeline.Spec.Environments {
		var envStatus v1alpha1.EnvironmentStatus
		envStatus.Targets = make([]v1alpha1.TargetStatus, len(env.Targets))
		envStatuses[env.Name] = &envStatus

		for i, target := range env.Targets {
			targetStatus := &envStatus.Targets[i]
			targetStatus.ClusterAppRef.LocalAppReference = pipeline.Spec.AppRef

			var clusterObject *clusterctrlv1alpha1.GitopsCluster
			if target.ClusterRef != nil {
				// record the fact of the remote cluster in the target status
				targetStatus.ClusterAppRef.ClusterRef = target.ClusterRef

				// even though we could just get the client from our list of clients, we check that the cluster object exists
				// every time. We might have been queued because of a cluster disappearing.

				var err error
				clusterObject, err = r.getCluster(ctx, pipeline, *target.ClusterRef)
				if err != nil {
					// emit the event whatever problem there was
					r.emitEventf(
						&pipeline,
						corev1.EventTypeWarning,
						"GetClusterError", "Failed to get cluster %s/%s for pipeline %s/%s: %s",
						target.ClusterRef.Namespace, target.ClusterRef.Name,
						pipeline.GetNamespace(), pipeline.GetName(),
						err,
					)

					// not found -- fine, maybe things are happening out of order; make a note and wait until the cluster exists (or something else happens).
					if apierrors.IsNotFound(err) {
						targetStatus.Error = err.Error()
						unready = true
						continue
					}

					// some other error -- this _is_ unexpected, so return it to controller-runtime.
					return ctrl.Result{}, err
				}

				if !conditions.IsReady(clusterObject.Status.Conditions) {
					msg := fmt.Sprintf("Target cluster '%s' not ready", target.ClusterRef.String())
					targetStatus.Error = msg
					unready = true
					continue
				}
			}

			targetObj, err := targetObject(&pipeline, &target)
			if err != nil {
				targetStatus.Error = fmt.Sprintf("target spec could not be interpreted as an object: %s", err.Error())
				unready = true
				continue
			}

			// it's OK if clusterObject is still `nil` -- that represents the local cluster.
			clusterClient, ok, err := r.watchTargetAndGetReader(ctx, clusterObject, targetObj)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !ok {
				targetStatus.Error = "Target cluster client is not synced"
				unready = true
				continue
			}

			targetKey := client.ObjectKeyFromObject(targetObj)
			// look up the actual application
			err = clusterClient.Get(ctx, targetKey, targetObj)
			if err != nil {
				r.emitEventf(
					&pipeline,
					corev1.EventTypeWarning,
					"GetAppError", "Failed to get application object %s%s/%s for pipeline %s/%s: %s",
					clusterPrefix(target.ClusterRef), targetKey.Namespace, targetKey.Name,
					pipeline.GetNamespace(), pipeline.GetName(),
					err,
				)

				targetStatus.Error = err.Error()
				unready = true
				continue
			}
			setTargetStatus(targetStatus, targetObj)
		}
	}

	var readyCondition metav1.Condition
	if unready {
		readyCondition = metav1.Condition{
			Type:    conditions.ReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  v1alpha1.TargetNotReadableReason,
			Message: "One or more targets was not reachable or not present",
		}
	} else {
		readyCondition = metav1.Condition{
			Type:    conditions.ReadyCondition,
			Status:  metav1.ConditionTrue,
			Reason:  v1alpha1.ReconciliationSucceededReason,
			Message: trimString("All clusters checked", v1alpha1.MaxConditionMessageLength),
		}
	}

	pipeline.Status.ObservedGeneration = pipeline.Generation
	apimeta.SetStatusCondition(&pipeline.Status.Conditions, readyCondition)
	pipeline.Status.Environments = envStatuses
	if err := r.patchStatus(ctx, client.ObjectKeyFromObject(&pipeline), pipeline.Status); err != nil {
		r.emitEventf(
			&pipeline,
			corev1.EventTypeWarning,
			"SetStatus", "Failed to patch status for pipeline %s/%s: %s",
			pipeline.GetNamespace(), pipeline.GetName(),
			err,
		)
		return ctrl.Result{Requeue: true}, err
	}
	r.emitEventf(
		&pipeline,
		corev1.EventTypeNormal,
		"Updated", "Updated pipeline %s/%s",
		pipeline.GetNamespace(), pipeline.GetName(),
	)

	firstEnv := pipeline.Spec.Environments[0]

	latestRevision := checkAllTargetsHaveSameRevision(pipeline.Status.Environments[firstEnv.Name])
	if latestRevision == "" {
		// not all targets have the same revision, or have no revision set, so we can't proceed
		if err := r.setPendingCondition(ctx, pipeline, v1alpha1.EnvironmentNotReadyReason, "Waiting for all targets to have the same revision"); err != nil {
			return ctrl.Result{Requeue: true}, fmt.Errorf("error setting pending condition: %w", err)
		}

		return ctrl.Result{}, nil
	}

	if !checkAllTargetsAreReady(pipeline.Status.Environments[firstEnv.Name]) {
		// not all targets are ready, so we can't proceed
		if err := r.setPendingCondition(ctx, pipeline, v1alpha1.EnvironmentNotReadyReason, "Waiting for all targets to be ready"); err != nil {
			return ctrl.Result{}, fmt.Errorf("error setting pending condition: %w", err)
		}

		return ctrl.Result{}, nil
	}

	if err := r.removePendingCondition(ctx, pipeline); err != nil {
		return ctrl.Result{}, fmt.Errorf("error removing pending condition: %w", err)
	}

	for _, env := range pipeline.Spec.Environments[1:] {
		// if all targets run the latest revision and are ready, we can skip this environment
		if checkAllTargetsRunRevision(pipeline.Status.Environments[env.Name], latestRevision) && checkAllTargetsAreReady(pipeline.Status.Environments[env.Name]) {
			continue
		}

		if checkAnyTargetHasRevision(pipeline.Status.Environments[env.Name], latestRevision) {
			return ctrl.Result{}, nil
		}

		err := r.promoteLatestRevision(ctx, pipeline, env, latestRevision)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error promoting new version: %w", err)
		}

		break
	}

	return ctrl.Result{}, nil
}

func (r *PipelineReconciler) setPendingCondition(ctx context.Context, pipeline v1alpha1.Pipeline, reason, message string) error {
	condition := metav1.Condition{
		Type:    conditions.PromotionPendingCondition,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}

	apimeta.SetStatusCondition(&pipeline.Status.Conditions, condition)

	if err := r.patchStatus(ctx, client.ObjectKeyFromObject(&pipeline), pipeline.Status); err != nil {
		return err
	}

	return nil
}

func (r *PipelineReconciler) removePendingCondition(ctx context.Context, pipeline v1alpha1.Pipeline) error {
	apimeta.RemoveStatusCondition(&pipeline.Status.Conditions, conditions.PromotionPendingCondition)

	if err := r.patchStatus(ctx, client.ObjectKeyFromObject(&pipeline), pipeline.Status); err != nil {
		return err
	}

	return nil
}

func (r *PipelineReconciler) promoteLatestRevision(ctx context.Context, pipeline v1alpha1.Pipeline, env v1alpha1.Environment, revision string) error {
	// none of the current strategies are idepontent, using it now to keep the ball rolling, but we need to implement
	// strategies that are.

	promotion := pipeline.Spec.GetPromotion(env.Name)
	if promotion == nil {
		return nil
	}

	strat, err := r.stratReg.Get(*promotion)
	if err != nil {
		return fmt.Errorf("error getting strategy from registry: %w", err)
	}

	prom := strategy.Promotion{
		PipelineName:      pipeline.Name,
		PipelineNamespace: pipeline.Namespace,
		Environment:       env,
		Version:           revision,
	}

	_, err = strat.Promote(ctx, *pipeline.Spec.Promotion, prom)

	return err
}

func checkAnyTargetHasRevision(env *v1alpha1.EnvironmentStatus, revision string) bool {
	for _, target := range env.Targets {
		if target.Revision == revision {
			return true
		}
	}

	return false
}

func checkAllTargetsRunRevision(env *v1alpha1.EnvironmentStatus, revision string) bool {
	for _, target := range env.Targets {
		if target.Revision != revision {
			return false
		}
	}

	return true
}

// checkAllTargetsHaveSameRevision returns a revision if all targets in the environment have the same,
// non-empty revision, and an empty string otherwise.
func checkAllTargetsHaveSameRevision(env *v1alpha1.EnvironmentStatus) string {
	if len(env.Targets) == 0 {
		return ""
	}

	revision := env.Targets[0].Revision
	for _, target := range env.Targets {
		if target.Revision != revision {
			return ""
		}
	}

	return revision
}

func checkAllTargetsAreReady(env *v1alpha1.EnvironmentStatus) bool {
	for _, target := range env.Targets {
		if !target.Ready {
			return false
		}
	}

	return true
}

// watchTargetAndGetReader ensures that the type (GroupVersionKind) of the target in the cluster given is being watched, and
// returns a client.Reader.
// A nil for the `clusterObject` argument indicates the local cluster. It returns an error if there was a problem, and otherwise
// a client.Reader and a bool which is true if the type was already watched and syncing.
//
// NB: the target object should be the object that is used as the `client.Object` argument for client.Get(...); the cache has different
// stores for {typed values, partial values, unstructured values}, and we want to install the watcher in the same one as we will later query.
func (r *PipelineReconciler) watchTargetAndGetReader(ctx context.Context, clusterObject *clusterctrlv1alpha1.GitopsCluster, target client.Object) (client.Reader, bool, error) {
	var clusterKey client.ObjectKey
	if clusterObject != nil {
		clusterKey = client.ObjectKeyFromObject(clusterObject)
	}

	targetGVK, err := apiutil.GVKForObject(target, r.targetScheme)
	if err != nil {
		return nil, false, err
	}
	cacheKey := clusterAndGVK{
		ObjectKey:        clusterKey,
		GroupVersionKind: targetGVK,
	}

	logger := r.manager.GetLogger().WithValues("component", "target-cache", "cluster", clusterKey, "type", targetGVK)

	r.cachesMu.Lock()
	cacheEntry, cacheFound := r.caches[cacheKey]
	r.cachesMu.Unlock()
	// To construct a cache, we need a *rest.Config. There's two ways to get one:
	//  - for the local cluster, we can just get the already prepared one from the Manager;
	//  - for a remote cluster, we can construct one given a kubeconfig; and the kubeconfig will be stored in a Secret,
	//    associated with the object representing the cluster.
	if !cacheFound {
		logger.Info("creating cache for cluster and type")
		var cfg *rest.Config

		if clusterObject == nil {
			cfg = r.manager.GetConfig()
		} else {
			var kubeconfig []byte
			var err error
			switch {
			case clusterObject.Spec.CAPIClusterRef != nil:
				var capiKey client.ObjectKey
				capiKey.Name = clusterObject.Spec.CAPIClusterRef.Name
				capiKey.Namespace = clusterObject.GetNamespace()
				kubeconfig, err = capicfg.FromSecret(ctx, r.Client, capiKey)
				if err != nil {
					return nil, false, err
				}
			case clusterObject.Spec.SecretRef != nil:
				var secretKey client.ObjectKey
				secretKey.Name = clusterObject.Spec.SecretRef.Name
				secretKey.Namespace = clusterObject.GetNamespace()

				var sec corev1.Secret
				if err := r.Get(ctx, secretKey, &sec); err != nil {
					return nil, false, err
				}
				var ok bool
				kubeconfig, ok = sec.Data["kubeconfig"]
				if !ok {
					return nil, false, fmt.Errorf("referenced Secret does not have data key %s", "kubeconfig")
				}
			default:
				return nil, false, fmt.Errorf("GitopsCluster object has neither .secretRef nor .capiClusterRef populated, unable to get remote cluster config")
			}
			cfg, err = clientcmd.RESTConfigFromKubeConfig(kubeconfig)
			if err != nil {
				return nil, false, err
			}
		}

		// having done all that, did we really need it?
		r.cachesMu.Lock()
		if cacheEntry, cacheFound = r.caches[cacheKey]; !cacheFound {
			c, err := cache.New(cfg, cache.Options{
				Scheme: r.targetScheme,
			})
			if err != nil {
				r.cachesMu.Unlock()
				return nil, false, err
			}

			cancel := r.runner.run(func(ctx context.Context) {
				if err := c.Start(ctx); err != nil {
					logger.Error(err, "cache exited with error")
				}
			})
			cacheEntry = cacheAndCancel{
				cache:  c,
				cancel: cancel,
			}
			r.caches[cacheKey] = cacheEntry
		}
		r.cachesMu.Unlock()
		// Add it to the queue for GC consideration
		r.cachesGC.register(cacheKey)
	}

	// Now we have a cache; make sure the object type in question is being watched, so we can query it and get updates.

	// The informer is retrieved whether we created the cache or not, because we want to know if it's synced and thus ready to be queried.
	typeCache := cacheEntry.cache
	inf, err := typeCache.GetInformer(ctx, target) // NB not InformerForKind(...), because that uses the typed value cache specifically (see the method comment).
	if err != nil {
		return nil, false, err
	}

	if !cacheFound { // meaning: we created the cache, this time around, so we'll need to install the event handler.
		enqueuePipelinesForTarget := func(obj interface{}) {
			eventObj, ok := obj.(client.Object)
			if !ok {
				logger.Info("value to look up in index was not a client.Object", "object", obj)
				return
			}
			pipelines, err := r.pipelinesForApplication(clusterKey, eventObj)
			if err != nil {
				logger.Error(err, "failed to look up pipelines in index of applications")
				return
			}
			// TODO is passing pointers here dangerous? (do they get copied, I think they might do). Alternative is to pass the whole list in the channel.
			for i := range pipelines {
				r.appEvents <- event.GenericEvent{Object: &pipelines[i]}
			}
		}

		_, err := inf.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				enqueuePipelinesForTarget(obj)
			},
			DeleteFunc: func(obj interface{}) {
				enqueuePipelinesForTarget(obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				// We're just looking up the name in the index so it'll be the same for the old as for the new object.
				// However, this might change elsewhere, so to be defensive, run both. The queue will deduplicate,
				// though it means we do a bit more lookup work.
				enqueuePipelinesForTarget(oldObj)
				enqueuePipelinesForTarget(newObj)
			},
		})
		if err != nil {
			return nil, false, err
		}
	}

	return typeCache, inf.HasSynced(), nil
}

// clusterPrefix returns a string naming the cluster containing an app, to prepend to the usual namespace/name format of the app object itself.
// So that it can be empty, the separator is include in the return value.
func clusterPrefix(ref *v1alpha1.CrossNamespaceClusterReference) string {
	if ref == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s:", ref.Namespace, ref.Name)
}

// targetObject returns a target object for a target spec, ready to be queried. The Pipeline is passed in as well as the Target,
// since the definition can be spread between these specs. This is coupled with setTargetStatus because the concrete types returned here
// must be handled by setTargetStatus.
func targetObject(pipeline *v1alpha1.Pipeline, target *v1alpha1.Target) (client.Object, error) {
	var obj unstructured.Unstructured
	gv, err := schema.ParseGroupVersion(pipeline.Spec.AppRef.APIVersion)
	if err != nil {
		return nil, err
	}
	gvk := gv.WithKind(pipeline.Spec.AppRef.Kind)
	obj.GetObjectKind().SetGroupVersionKind(gvk)
	obj.SetName(pipeline.Spec.AppRef.Name)
	obj.SetNamespace(target.Namespace)
	return &obj, nil
}

// setTargetStatus gets the relevant status from the app object given, and records it in the TargetStatus.
func setTargetStatus(status *v1alpha1.TargetStatus, targetObject client.Object) {
	switch obj := targetObject.(type) {
	case *unstructured.Unstructured:
		// this assumes it's a Flux-like object; specifically with
		//   - a Ready condition
		//   - a .status.lastAppliedRevision
		conds, ok, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
		if !ok || err != nil {
			status.Ready = false
			return
		}
		status.Ready = conditions.IsReadyUnstructured(conds)

		lastAppliedRev, ok, err := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
		if !ok || err != nil {
			// It's not an error to lack a Ready condition (new objects will lack any conditions), and it's not an error to lack a lastAppliedRevision
			// (maybe it hasn't got that far yet); but it is an error to have a ready condition of true and lack a lastAppliedRevision, since that means
			// the object is not a usable target.
			if status.Ready {
				status.Error = "unable to find .status.lastAppliedRevision in ready target object"
				status.Ready = false
				return
			}
		} else {
			status.Revision = lastAppliedRev
		}
	default:
		status.Error = "unable to determine ready status for object"
		status.Ready = false
	}
}

func (r *PipelineReconciler) patchStatus(ctx context.Context, n types.NamespacedName, newStatus v1alpha1.PipelineStatus) error {
	var pipeline v1alpha1.Pipeline
	if err := r.Get(ctx, n, &pipeline); err != nil {
		return err
	}

	patch := client.MergeFrom(pipeline.DeepCopy())
	pipeline.Status = newStatus
	return r.Status().Patch(ctx, &pipeline, patch, client.FieldOwner(r.ControllerName))
}

func (r *PipelineReconciler) getCluster(ctx context.Context, p v1alpha1.Pipeline, clusterRef v1alpha1.CrossNamespaceClusterReference) (*clusterctrlv1alpha1.GitopsCluster, error) {
	cluster := &clusterctrlv1alpha1.GitopsCluster{}
	namespace := clusterRef.Namespace
	if clusterRef.Namespace == "" {
		namespace = p.Namespace
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: clusterRef.Name}, cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

// == Garbage collection of caches ==

// cacheIndex names the index for keeping track of which pipelines use which caches.
const cacheIndex = "cache"

// This gives a string representation of a key into the caches map, so it can be used for indexing.
// It has the signature of `targetIndexerFunc` so it can be used with `indexTargets(...)`.
// isCacheUsed below relies on this implementation using clusterAndGVK.String(), so that
// the index keys it uses match what's actually indexed.
func clusterCacheKey(clusterKey client.ObjectKey, gvk schema.GroupVersionKind, _targetKey client.ObjectKey) string {
	return (clusterAndGVK{
		ObjectKey:        clusterKey,
		GroupVersionKind: gvk,
	}).String()
}

// indexTargetCache is an IndexerFunc that returns a key representing the cache a target will come from. This is coupled to
// the scheme for keeping track of caches, as embodied in the `caches` map in the reconciler the method `watchTargetAndGetReader`;
// changing how those work, for instance caching by {cluster, namespace, type} instead,  will likely need a change here.
var indexTargetCache = indexTargets(clusterCacheKey)

// isCacheUsed looks up the given cache key, and returns true if a pipeline uses that cache, and false otherwise; or,
// an error if the query didn't succeed.
func (r *PipelineReconciler) isCacheUsed(key clusterAndGVK) (bool, error) {
	var list v1alpha1.PipelineList
	if err := r.List(context.TODO(), &list, client.MatchingFields{
		cacheIndex: key.String(),
	}); err != nil {
		return false, err
	}
	return len(list.Items) > 0, nil
}

func (r *PipelineReconciler) removeCache(key clusterAndGVK) {
	r.cachesMu.Lock()
	defer r.cachesMu.Unlock()
	if entry, ok := r.caches[key]; ok {
		entry.cancel()
		delete(r.caches, key)
	}
}

type cachesInterface interface {
	isCacheUsed(clusterAndGVK) (bool, error)
	removeCache(clusterAndGVK)
}

type gc struct {
	caches cachesInterface
	queue  workqueue.RateLimitingInterface
	log    logr.Logger
}

func newGC(caches cachesInterface, logger logr.Logger) *gc {
	ratelimiter := workqueue.NewItemExponentialFailureRateLimiter(2*time.Second, 512*time.Second)
	queue := workqueue.NewRateLimitingQueueWithConfig(ratelimiter, workqueue.RateLimitingQueueConfig{
		Name: "cache-garbage-collection",
	})

	return &gc{
		caches: caches,
		queue:  queue,
		log:    logger,
	}
}

func (gc *gc) register(key clusterAndGVK) {
	gc.log.Info("cache key registered for GC", "key", key)
	gc.queue.Add(key) // NB not rate limited. Though, this is called when the key is introduced, so it shouldn't matter one way or the other.
}

func (gc *gc) loop() {
	for {
		item, shutdown := gc.queue.Get()
		if shutdown {
			return
		}
		key, ok := item.(clusterAndGVK)
		if !ok {
			gc.queue.Forget(item)
			gc.queue.Done(item)
		}

		if ok, err := gc.caches.isCacheUsed(key); err != nil {
			gc.log.Error(err, "calling isCacheUsed", "key", key)
		} else if ok {
			// still used, requeue for consideration
			gc.queue.Done(key)
			gc.queue.AddRateLimited(key)
		} else {
			gc.log.Info("removing unused cache", "key", key, "requeues", gc.queue.NumRequeues(key))
			gc.queue.Forget(key)
			gc.queue.Done(key)
			gc.caches.removeCache(key)
		}
	}
}

func (gc *gc) Start(ctx context.Context) error {
	if gc.caches == nil || gc.queue == nil {
		return fmt.Errorf("neither of caches nor queue can be nil")
	}
	// queue.Get blocks until either there's an item, or the queue is shutting down, so we can't put
	// it in a select with <-ctx.Done(). Instead, do this bit in a goroutine, and rely on it exiting
	// when the queue is shut down.
	go gc.loop()
	<-ctx.Done()
	gc.log.Info("shutting down")
	gc.queue.ShutDown()
	return nil
}

// == Setup of indices and static watchers ==

// SetupWithManager sets up the controller with the Manager.
func (r *PipelineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.manager = mgr

	r.runner = newRunner()
	if err := mgr.Add(r.runner); err != nil {
		return err
	}

	r.cachesGC = newGC(r, mgr.GetLogger().WithValues("component", "target-cache-gc"))
	if err := mgr.Add(r.cachesGC); err != nil {
		return err
	}

	const (
		gitopsClusterIndexKey string = ".spec.environment.ClusterRef" // this is arbitrary, but let's make it suggest what it's indexing.
	)
	// Index the Pipelines by the GitopsCluster references they (may) point at.
	if err := mgr.GetCache().IndexField(context.TODO(), &v1alpha1.Pipeline{}, gitopsClusterIndexKey,
		r.indexClusterKind("GitopsCluster")); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	// Index the Pipelines by the application references they point at.
	if err := mgr.GetCache().IndexField(context.TODO(), &v1alpha1.Pipeline{}, applicationKey, indexApplication /* <- both from indexing.go */); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	// Index the Pipelines by the cache they require for their targets.
	if err := mgr.GetCache().IndexField(context.TODO(), &v1alpha1.Pipeline{}, cacheIndex, indexTargetCache); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	if r.recorder == nil {
		r.recorder = mgr.GetEventRecorderFor(r.ControllerName)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Pipeline{}).
		Watches(
			&clusterctrlv1alpha1.GitopsCluster{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForCluster(gitopsClusterIndexKey)),
		).
		WatchesRawSource(&source.Channel{Source: r.appEvents}, &handler.EnqueueRequestForObject{}).
		Complete(r)
}

func (r *PipelineReconciler) emitEventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	if r.recorder == nil {
		return
	}

	r.recorder.Eventf(object, eventtype, reason, messageFmt, args...)
}

func trimString(str string, limit int) string {
	if len(str) <= limit {
		return str
	}

	return str[0:limit] + "..."
}
