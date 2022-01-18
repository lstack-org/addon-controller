package main

import (
	"context"
	"flag"
	"github.com/lstack-org/addon-controller/app"
	"github.com/lstack-org/addon-controller/config"
	clientset "github.com/lstack-org/addon-controller/pkg/generated/clientset/versioned"
	informers "github.com/lstack-org/addon-controller/pkg/generated/informers/externalversions"
	controllerhealthz "github.com/lstack-org/addon-controller/pkg/healthz"
	"github.com/lstack-org/addon-controller/pkg/signals"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	apiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/healthz"
	"k8s.io/apiserver/pkg/server/mux"
	apiserveroptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"net"
	"os"
	"time"
)

var (
	masterURL  string
	kubeconfig string
	refresh    int64
)

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&masterURL, "master", "", "The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
	flag.Int64Var(&refresh, "refresh", 60, "local work queue refresh time duration")
}

func main() {
	flag.Parse()
	Run(kubeconfig)
}

func Run(path string) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		klog.Fatalf("%s", err)
	}
	kubeClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		klog.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}
	ip := net.ParseIP("127.0.0.1")
	loopback := apiserveroptions.NewSecureServingOptions().WithLoopback()
	loopback.ServerCert.CertDirectory = ""
	loopback.ServerCert.PairName = "lstack-addon-controller"
	loopback.BindPort = 10288
	if e := loopback.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{ip}); e != nil {
		klog.Fatalf("%s", err)
	}
	var loopbackClientConfig *restclient.Config
	var secureServing *apiserver.SecureServingInfo
	err = loopback.ApplyTo(&secureServing, &loopbackClientConfig)
	if err != nil {
		klog.Fatalf("%s", err)
	}
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})
	cfg := &config.Config{
		LeaderElection: config.LeaderElectionConfiguration{
			LeaderElect:       true,
			LeaseDuration:     metav1.Duration{Duration: time.Second * 15},
			RenewDeadline:     metav1.Duration{Duration: time.Second * 10},
			RetryPeriod:       metav1.Duration{Duration: time.Second * 2},
			ResourceLock:      "leases",
			ResourceName:      "lstack-addon-controller-manager",
			ResourceNamespace: "kube-system",
		},
		LoopbackClientConfig: loopbackClientConfig,
		Client:               kubeClient,
		Kubeconfig:           restCfg,
		EventRecorder:        recorder,
		SecureServing:        secureServing,
		Authentication:       apiserver.AuthenticationInfo{},
		Authorization:        apiserver.AuthorizationInfo{},
	}
	stopCh := signals.SetupSignalHandler()
	err = run(cfg.Complete(), stopCh)
	if err != nil {
		klog.Fatalf("%s", err)
	}
}

func run(c *config.CompletedConfig, stopCh <-chan struct{}) error {
	var checks []healthz.HealthChecker
	var electionChecker *leaderelection.HealthzAdaptor
	if c.LeaderElection.LeaderElect {
		electionChecker = leaderelection.NewLeaderHealthzAdaptor(time.Second * 20)
		checks = append(checks, electionChecker)
	}
	healthHandler := controllerhealthz.NewMutableHealthzHandler(checks...)
	var unsecuredMux *mux.PathRecorderMux
	if c.SecureServing != nil {
		unsecuredMux = app.NewBaseHandler(healthHandler)
		handler := app.BuildHandlerChain(unsecuredMux, &c.Authorization, &c.Authentication)
		if _, err := c.SecureServing.Serve(handler, 0, stopCh); err != nil {
			return err
		}
	}

	startController := func() {

		addonClient, err := clientset.NewForConfig(c.Kubeconfig)
		if err != nil {
			klog.Fatalf("Error building lstack addon clientset: %s", err.Error())
		}
		// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(stopCh)
		// Start method is non-blocking and runs all registered informers in a dedicated goroutine.

		addonInformerFactory := informers.NewSharedInformerFactory(addonClient, time.Second*time.Duration(refresh))
		controller := NewController(c.Client, addonClient, addonInformerFactory.Lstack().V1().Addons(), c.Kubeconfig)

		addonInformerFactory.Start(stopCh)
		if err = controller.Run(2, stopCh); err != nil {
			klog.Fatalf("Error running controller: %s", err.Error())
		}
	}

	if !c.LeaderElection.LeaderElect {
		startController()
	}

	id, err := os.Hostname()
	if err != nil {
		return err
	}

	// add a uniquifier so that two processes on the same host don't accidentally both become active
	id = id + "_" + string(uuid.NewUUID())
	go leaderElectAndRun(c, id, electionChecker,
		c.LeaderElection.ResourceLock,
		c.LeaderElection.ResourceName,
		leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				startController()
			},
			OnStoppedLeading: func() {
				klog.Fatalf("migration leaderelection lost")
			},
		})
	select {}
}

func leaderElectAndRun(c *config.CompletedConfig, lockIdentity string, electionChecker *leaderelection.HealthzAdaptor, resourceLock string, leaseName string, callbacks leaderelection.LeaderCallbacks) {
	rl, err := NewFromKubeconfig(resourceLock,

		c.LeaderElection.ResourceNamespace,
		leaseName,
		resourcelock.ResourceLockConfig{
			Identity:      lockIdentity,
			EventRecorder: c.EventRecorder,
		},
		c.Kubeconfig,
		c.LeaderElection.RenewDeadline.Duration,
	)
	if err != nil {
		klog.Fatalf("error creating lock: %v", err)
	}
	leaderelection.RunOrDie(context.TODO(), leaderelection.LeaderElectionConfig{
		Lock:          rl,
		LeaseDuration: c.LeaderElection.LeaseDuration.Duration,
		RenewDeadline: c.LeaderElection.RenewDeadline.Duration,
		RetryPeriod:   c.LeaderElection.RetryPeriod.Duration,
		Callbacks:     callbacks,
		WatchDog:      electionChecker,
		Name:          leaseName,
	})
	panic("unreachable")
}

func NewFromKubeconfig(lockType string, ns string, name string, rlc resourcelock.ResourceLockConfig, kubeconfig *restclient.Config, renewDeadline time.Duration) (resourcelock.Interface, error) {
	// shallow copy, do not modify the kubeconfig
	cfg := *kubeconfig
	timeout := renewDeadline / 2
	if timeout < time.Second {
		timeout = time.Second
	}
	cfg.Timeout = timeout
	leaderElectionClient := kubernetes.NewForConfigOrDie(restclient.AddUserAgent(&cfg, "leader-election"))
	return resourcelock.New(lockType, ns, name, leaderElectionClient.CoreV1(), leaderElectionClient.CoordinationV1(), rlc)
}
