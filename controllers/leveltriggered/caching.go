package leveltriggered

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
	capicfg "sigs.k8s.io/cluster-api/util/kubeconfig"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
)

// caches holds all the values needed for keeping track of client caches, used for 1. querying clusters for arbitrary app objects; and,
// 2. installing watches so that app object updates will trigger reconciliation of the pipelines that use them.
type caches struct {
	// used to convey events to the reconciler
	events chan event.GenericEvent

	// internal bookkeeping
	targetScheme *runtime.Scheme
	cachesMap    map[clusterAndGVK]cacheAndCancel
	cachesMu     *sync.Mutex

	// these are constructed when this object is set up with a manager
	baseLogger         logr.Logger
	reader             client.Reader
	localClusterConfig *rest.Config
	cachesGC           *gc
	runner             *runner
}

func newCaches(targetScheme *runtime.Scheme) *caches {
	events := make(chan event.GenericEvent)
	return &caches{
		targetScheme: targetScheme,
		events:       events,
		cachesMap:    make(map[clusterAndGVK]cacheAndCancel),
		cachesMu:     &sync.Mutex{},
	}
}

func (c *caches) appEvents() <-chan event.GenericEvent {
	return c.events
}

func (c *caches) setupWithManager(mgr ctrl.Manager) error {
	c.localClusterConfig = mgr.GetConfig()
	c.baseLogger = mgr.GetLogger().WithValues("component", "target-cache")
	c.reader = mgr.GetClient() // this specifically gets the client that has the indexing installed below; i.e., these are coupled.

	c.runner = newRunner()
	if err := mgr.Add(c.runner); err != nil {
		return err
	}

	c.cachesGC = newGC(c, mgr.GetLogger().WithValues("component", "target-cache-gc"))
	if err := mgr.Add(c.cachesGC); err != nil {
		return err
	}

	// Index the Pipelines by the cache they require for their targets.
	if err := mgr.GetCache().IndexField(context.TODO(), &v1alpha1.Pipeline{}, cacheIndex, indexTargetCache); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	// Index the Pipelines by the application references they point at.
	if err := mgr.GetCache().IndexField(context.TODO(), &v1alpha1.Pipeline{}, applicationKey, indexApplication /* <- both from indexing.go */); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	return nil
}

func (c *caches) getCacheKeys() (res []clusterAndGVK) {
	c.cachesMu.Lock()
	defer c.cachesMu.Unlock()
	for k := range c.cachesMap {
		res = append(res, k)
	}
	return res
}

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

// watchTargetAndGetReader ensures that the type (GroupVersionKind) of the target in the cluster given is being watched, and
// returns a client.Reader.
// A nil for the `clusterObject` argument indicates the local cluster. It returns an error if there was a problem, and otherwise
// a client.Reader and a bool which is true if the type was already watched and syncing.
//
// NB: the target object should be the object that is used as the `client.Object` argument for client.Get(...); the cache has different
// stores for {typed values, partial values, unstructured values}, and we want to install the watcher in the same one as we will later query.
func (c *caches) watchTargetAndGetReader(ctx context.Context, clusterObject *clusterctrlv1alpha1.GitopsCluster, target client.Object) (client.Reader, bool, error) {
	var clusterKey client.ObjectKey
	if clusterObject != nil {
		clusterKey = client.ObjectKeyFromObject(clusterObject)
	}

	targetGVK, err := apiutil.GVKForObject(target, c.targetScheme)
	if err != nil {
		return nil, false, err
	}
	cacheKey := clusterAndGVK{
		ObjectKey:        clusterKey,
		GroupVersionKind: targetGVK,
	}

	logger := c.baseLogger.WithValues("cluster", clusterKey, "type", targetGVK)

	c.cachesMu.Lock()
	cacheEntry, cacheFound := c.cachesMap[cacheKey]
	c.cachesMu.Unlock()
	// To construct a cache, we need a *rest.Config. There's two ways to get one:
	//  - for the local cluster, we can just get the already prepared one from the Manager;
	//  - for a remote cluster, we can construct one given a kubeconfig; and the kubeconfig will be stored in a Secret,
	//    associated with the object representing the cluster.
	if !cacheFound {
		logger.Info("creating cache for cluster and type")
		var cfg *rest.Config

		if clusterObject == nil {
			cfg = c.localClusterConfig
		} else {
			var kubeconfig []byte
			var err error
			switch {
			case clusterObject.Spec.CAPIClusterRef != nil:
				var capiKey client.ObjectKey
				capiKey.Name = clusterObject.Spec.CAPIClusterRef.Name
				capiKey.Namespace = clusterObject.GetNamespace()
				kubeconfig, err = capicfg.FromSecret(ctx, c.reader, capiKey)
				if err != nil {
					return nil, false, err
				}
			case clusterObject.Spec.SecretRef != nil:
				var secretKey client.ObjectKey
				secretKey.Name = clusterObject.Spec.SecretRef.Name
				secretKey.Namespace = clusterObject.GetNamespace()

				var sec corev1.Secret
				if err := c.reader.Get(ctx, secretKey, &sec); err != nil {
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
		c.cachesMu.Lock()
		if cacheEntry, cacheFound = c.cachesMap[cacheKey]; !cacheFound {
			ca, err := cache.New(cfg, cache.Options{
				Scheme: c.targetScheme,
			})
			if err != nil {
				c.cachesMu.Unlock()
				return nil, false, err
			}

			cancel := c.runner.run(func(ctx context.Context) {
				if err := ca.Start(ctx); err != nil {
					logger.Error(err, "cache exited with error")
				}
			})
			cacheEntry = cacheAndCancel{
				cache:  ca,
				cancel: cancel,
			}
			c.cachesMap[cacheKey] = cacheEntry
		}
		c.cachesMu.Unlock()
		// Add it to the queue for GC consideration
		c.cachesGC.register(cacheKey)
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
			pipelines, err := c.pipelinesForApplication(clusterKey, eventObj)
			if err != nil {
				logger.Error(err, "failed to look up pipelines in index of applications")
				return
			}
			// TODO is passing pointers here dangerous? (do they get copied, I think they might do). Alternative is to pass the whole list in the channel.
			for i := range pipelines {
				c.events <- event.GenericEvent{Object: &pipelines[i]}
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

// == target indexing ==
//
// Each target in a pipeline is put in an index, so that given an event referring to that target, the pipeline(s) using it can
// be looked up and queued for reconciliation.

// targetIndexKey produces an index key for the exact target
func targetIndexKey(clusterKey client.ObjectKey, gvk schema.GroupVersionKind, targetKey client.ObjectKey) string {
	key := fmt.Sprintf("%s:%s:%s", clusterKey, gvk, targetKey)
	return key
}

// indexApplication extracts all the application refs from a pipeline. The index keys are
//
//	<cluster namespace>/<cluster name>:<group>/<kind>/<namespace>/<name>`.
var indexApplication = indexTargets(targetIndexKey)

// pipelinesForApplication is given an application object and its cluster, and looks up the pipeline(s) that use it as a target.
// It assumes applications are indexed using `indexApplication(...)` (or something using the same key format and index).
// `clusterName` can be a zero value, but if it's not, both the namespace and name should be supplied (since the namespace will
// be give a value if there's only a name supplied, when indexing. See `indexApplication()`).
func (c *caches) pipelinesForApplication(clusterName client.ObjectKey, obj client.Object) ([]v1alpha1.Pipeline, error) {
	ctx := context.Background()
	var list v1alpha1.PipelineList
	gvk, err := apiutil.GVKForObject(obj, c.targetScheme)
	if err != nil {
		return nil, err
	}

	key := targetIndexKey(clusterName, gvk, client.ObjectKeyFromObject(obj))
	if err := c.reader.List(ctx, &list, client.MatchingFields{
		applicationKey: key,
	}); err != nil {
		return nil, err
	}

	return list.Items, nil
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
func (c *caches) isCacheUsed(key clusterAndGVK) (bool, error) {
	var list v1alpha1.PipelineList
	if err := c.reader.List(context.TODO(), &list, client.MatchingFields{
		cacheIndex: key.String(),
	}); err != nil {
		return false, err
	}
	return len(list.Items) > 0, nil
}

// removeCache stops the cache identified by `key` running.
func (c *caches) removeCache(key clusterAndGVK) {
	c.cachesMu.Lock()
	defer c.cachesMu.Unlock()
	if entry, ok := c.cachesMap[key]; ok {
		entry.cancel()
		delete(c.cachesMap, key)
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
	gc.log.Info("cache key registered for GC", "key", key.String())
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
