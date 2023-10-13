package main

import (
	"fmt"
	"os"
	"time"

	"github.com/weaveworks/pipeline-controller/server/strategy/pullrequest"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/fluxcd/pkg/runtime/events"
	"github.com/fluxcd/pkg/runtime/logger"
	flag "github.com/spf13/pflag"
	clusterctrlv1alpha1 "github.com/weaveworks/cluster-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/weaveworks/pipeline-controller/api/v1alpha1"
	"github.com/weaveworks/pipeline-controller/controllers"
	"github.com/weaveworks/pipeline-controller/controllers/leveltriggered"
	"github.com/weaveworks/pipeline-controller/server"
	"github.com/weaveworks/pipeline-controller/server/strategy"
	"github.com/weaveworks/pipeline-controller/server/strategy/notification"
)

const (
	controllerName = "pipeline-controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(clusterctrlv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr                       string
		eventsAddr                        string
		enableLeaderElection              bool
		probeAddr                         string
		promServerAddr                    string
		logOptions                        logger.Options
		promotionRateLimit                int
		promotionRateLimitIntervalSeconds int
		promotionRetryDelaySeconds        int
		promotionRetryMaxDelaySeconds     int
		promotionRetryFailureThreshold    int
		useLevelTriggeredController       bool
	)

	// This is a feature flag, guarding the new level-triggered behaviour.
	flag.BoolVar(&useLevelTriggeredController, "enable-level-triggered", false, "when true, the controller will use level-triggering rather than relying on notifications")

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&eventsAddr, "events-addr", "", "The address of the events receiver.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&promServerAddr, "promotion-hook-bind-address", ":8082", "The address the promotion webhook server endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	// Rate limit
	flag.IntVar(&promotionRateLimit, "promotion-hook-rate-limit", server.DefaultRateLimitCount, "Promotion webhook rate limit, maximum number of requests in set interval.")
	flag.IntVar(&promotionRateLimitIntervalSeconds, "promotion-hook-rate-limit-interval", server.DefaultRateLimitInterval, "Promotion webhook rate limit interval.")

	// Retry
	flag.IntVar(&promotionRetryDelaySeconds, "promotion-retry-delay", server.DefaultRetryDelay, "Delay between promotion retries in seconds.")
	flag.IntVar(&promotionRetryMaxDelaySeconds, "promotion-retry-max-delay", server.DefaultRetryMaxDelay, "Maximum delay between promotion retries.")
	flag.IntVar(&promotionRetryFailureThreshold, "promotion-retry-threshold", server.DefaultRetryThreshold, "How many times a promotion should be retried.")

	logOptions.BindFlags(flag.CommandLine)

	flag.Parse()

	log := logger.NewLogger(logOptions)
	ctrl.SetLogger(log)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       fmt.Sprintf("%s-leader-election", controllerName),
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	pullRequestStrategy, err := pullrequest.New(
		mgr.GetClient(),
		log.WithValues("strategy", "pullrequest"),
	)
	if err != nil {
		setupLog.Error(err, "unable to create GitHub promotion strategy")
		os.Exit(1)
	}

	var eventRecorder *events.Recorder
	if eventRecorder, err = events.NewRecorder(mgr, ctrl.Log, eventsAddr, controllerName); err != nil {
		setupLog.Error(err, "unable to create event recorder")
		os.Exit(1)
	}
	notificationStrat, _ := notification.NewNotification(mgr.GetClient(), eventRecorder)

	var stratReg strategy.StrategyRegistry
	stratReg.Register(pullRequestStrategy)
	stratReg.Register(notificationStrat)

	var startErr error
	if useLevelTriggeredController {
		startErr = leveltriggered.NewPipelineReconciler(
			mgr.GetClient(),
			mgr.GetScheme(),
			controllerName,
			stratReg,
		).SetupWithManager(mgr)
	} else {
		startErr = controllers.NewPipelineReconciler(
			mgr.GetClient(),
			mgr.GetScheme(),
			controllerName,
		).SetupWithManager(mgr)
	}

	if startErr != nil {
		setupLog.Error(startErr, "unable to create controller", "controller", "Pipeline")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	promServer, err := server.NewPromotionServer(
		mgr.GetClient(),
		server.WithRateLimit(promotionRateLimit, time.Duration(promotionRateLimitIntervalSeconds)*time.Second),
		server.WithRetry(promotionRetryDelaySeconds, promotionRetryMaxDelaySeconds, promotionRetryFailureThreshold),
		server.Logger(log.WithName("promotion")),
		server.ListenAddr(promServerAddr),
		server.StrategyRegistry(stratReg),
	)
	if err != nil {
		setupLog.Error(err, "failed setting up promotion server")
		os.Exit(1)
	}
	go func() {
		if err := promServer.Start(ctx); err != nil {
			setupLog.Error(err, "problem running promotion server")
			os.Exit(1)
		}
	}()

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
