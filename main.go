package main

import (
	"flag"
	"os"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	eipv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/eipoperator/v1alpha1"
	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
	nlbpov1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/pkg/controller"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/pkg/provider"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = nlbpov1alpha1.AddToScheme(scheme)
	_ = nlbv1.SchemeBuilder.AddToScheme(scheme)
	_ = eipv1.AddToScheme(scheme)
}

func main() {
	var (
		metricsAddr          string
		enableLeaderElection bool
		probeAddr            string
		addServersQPS        float64
		removeServersQPS     float64
		kubeAPIQPS           float64
		kubeAPIBurst         int
		createSGQPS          float64
		createListenerQPS    float64
		getListenerQPS       float64
		deleteListenerQPS    float64
		deleteSGQPS          float64
		listListenersByPortQPS float64
		listServerGroupServersQPS float64
		listListenersQPS     float64
		getServerGroupAttributeQPS float64
		getJobStatusQPS      float64
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Float64Var(&addServersQPS, "add-servers-qps", 0,
		"QPS limit for AddServersToServerGroup. 0 disables local limiting.")
	flag.Float64Var(&removeServersQPS, "remove-servers-qps", 0,
		"QPS limit for RemoveServersFromServerGroup. 0 disables local limiting.")
	flag.Float64Var(&kubeAPIQPS, "kube-api-qps", 50,
		"QPS for requests to the Kubernetes API server.")
	flag.IntVar(&kubeAPIBurst, "kube-api-burst", 100,
		"Burst for requests to the Kubernetes API server.")
	flag.Float64Var(&createSGQPS, "create-sg-qps", 5.0,
		"QPS limit for CreateServerGroup. 0 disables local limiting.")
	flag.Float64Var(&createListenerQPS, "create-listener-qps", 5.0,
		"QPS limit for CreateListener. 0 disables local limiting.")
	flag.Float64Var(&getListenerQPS, "get-listener-qps", 20.0,
		"QPS limit for GetListenerAttribute. 0 disables local limiting.")
	flag.Float64Var(&deleteListenerQPS, "delete-listener-qps", 3.0,
		"QPS limit for DeleteListener. 0 disables local limiting.")
	flag.Float64Var(&deleteSGQPS, "delete-sg-qps", 3.0,
		"QPS limit for DeleteServerGroup. 0 disables local limiting.")
	flag.Float64Var(&listListenersByPortQPS, "list-listeners-by-port-qps", 5.0,
		"QPS limit for ListListenersByPort. 0 disables local limiting.")
	flag.Float64Var(&listServerGroupServersQPS, "list-server-group-servers-qps", 20.0,
		"QPS limit for ListServerGroupServers. 0 disables local limiting.")
	flag.Float64Var(&listListenersQPS, "list-listeners-qps", 20.0,
		"QPS limit for ListListeners. 0 disables local limiting.")
	flag.Float64Var(&getServerGroupAttributeQPS, "get-server-group-attribute-qps", 20.0,
		"QPS limit for GetServerGroupAttribute. 0 disables local limiting.")
	flag.Float64Var(&getJobStatusQPS, "get-job-status-qps", 20.0,
		"QPS limit for GetJobStatus. 0 disables local limiting.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	cfg := ctrl.GetConfigOrDie()
	cfg.QPS = float32(kubeAPIQPS)
	cfg.Burst = kubeAPIBurst

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "nlb-pool-operator.alibabacloud.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Initialize the Alibaba Cloud NLB API client used by PortAllocationReconciler.
	accessKeyId := os.Getenv("ACCESS_KEY_ID")
	accessKeySecret := os.Getenv("ACCESS_KEY_SECRET")
	regionId := os.Getenv("REGION_ID")
	if accessKeyId == "" || accessKeySecret == "" || regionId == "" {
		setupLog.Error(nil, "ACCESS_KEY_ID / ACCESS_KEY_SECRET / REGION_ID env vars are required")
		os.Exit(1)
	}
	nlbClient, err := provider.NewAlibabaNLBClient(accessKeyId, accessKeySecret, regionId)
	if err != nil {
		setupLog.Error(err, "unable to create Alibaba Cloud NLB API client")
		os.Exit(1)
	}

	// Wrap with per-interface token-bucket rate limiter. Only the write APIs
	// known to be throttled by Alibaba Cloud (AddServers / RemoveServers,
	// 200/60s ~= 3.3 QPS) are limited; all other interfaces pass through.
	rateLimitedClient := provider.NewRateLimitedClient(nlbClient)
	if addServersQPS > 0 { rateLimitedClient.AddServersLimiter = rate.NewLimiter(rate.Limit(addServersQPS), 5) }
	if removeServersQPS > 0 { rateLimitedClient.RemoveServersLimiter = rate.NewLimiter(rate.Limit(removeServersQPS), 5) }
	if createSGQPS > 0 { rateLimitedClient.CreateSGLimiter = rate.NewLimiter(rate.Limit(createSGQPS), 3) }
	if createListenerQPS > 0 { rateLimitedClient.CreateListenerLimiter = rate.NewLimiter(rate.Limit(createListenerQPS), 3) }
	if getListenerQPS > 0 { rateLimitedClient.GetListenerLimiter = rate.NewLimiter(rate.Limit(getListenerQPS), 10) }
	if deleteListenerQPS > 0 { rateLimitedClient.DeleteListenerLimiter = rate.NewLimiter(rate.Limit(deleteListenerQPS), 3) }
	if deleteSGQPS > 0 { rateLimitedClient.DeleteSGLimiter = rate.NewLimiter(rate.Limit(deleteSGQPS), 3) }
	if listListenersByPortQPS > 0 { rateLimitedClient.ListListenersByPortLimiter = rate.NewLimiter(rate.Limit(listListenersByPortQPS), 5) }
	if listServerGroupServersQPS > 0 { rateLimitedClient.ListSGLimiter = rate.NewLimiter(rate.Limit(listServerGroupServersQPS), 10) }
	if listListenersQPS > 0 { rateLimitedClient.ListListenersLimiter = rate.NewLimiter(rate.Limit(listListenersQPS), 10) }
	if getServerGroupAttributeQPS > 0 { rateLimitedClient.GetSGAttrLimiter = rate.NewLimiter(rate.Limit(getServerGroupAttributeQPS), 10) }
	if getJobStatusQPS > 0 { rateLimitedClient.GetJobLimiter = rate.NewLimiter(rate.Limit(getJobStatusQPS), 10) }

	// NLBPool Controller - orchestrator that uses cloud API for deletion verification.
	if err = (&controller.NLBPoolReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor("nlbpool-controller"),
		NLBClient: rateLimitedClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NLBPool")
		os.Exit(1)
	}

	// PortAllocation Controller - drives AddServers / RemoveServers via NLB API.
	if err = (&controller.PortAllocationReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		NLBClient: rateLimitedClient,
		Recorder:  mgr.GetEventRecorderFor("portallocation-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PortAllocation")
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

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
