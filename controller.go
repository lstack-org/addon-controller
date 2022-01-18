package main

import (
	"context"
	"encoding/json"
	"fmt"
	addonv1 "github.com/lstack-org/addon-controller/pkg/apis/addoncontroller/v1"
	clientset "github.com/lstack-org/addon-controller/pkg/generated/clientset/versioned"
	addonscheme "github.com/lstack-org/addon-controller/pkg/generated/clientset/versioned/scheme"
	informers "github.com/lstack-org/addon-controller/pkg/generated/informers/externalversions/addoncontroller/v1"
	addonlisters "github.com/lstack-org/addon-controller/pkg/generated/listers/addoncontroller/v1"
	"github.com/lstack-org/addon-controller/pkg/helm"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"reflect"
	"strings"
	"time"
)

const controllerAgentName = "addon-controller"

const (
	NotFound = "not found"
	// SuccessSynced is used as part of the Event 'reason' when a Foo is synced
	SuccessSynced = "Synced"
	// ErrResourceExists is used as part of the Event 'reason' when a Foo fails
	// to sync due to a Deployment of the same name already existing.
	ErrResourceExists    = "ErrAddonExists"
	ErrorUpgradeFail     = "ErrorUpgradeFail"
	ErrorInstallFail     = "ErrorInstallError"
	ErrorHealthCheckFail = "ErrorHealthCheckFail"
	// MessageResourceExists is the message used for Events when a resource
	// fails to sync due to a Deployment already existing
	MessageResourceExists = "Resource %q already exists and is not managed by Addon"
	// MessageResourceSynced is the message used for an Event fired when a Foo
	// is synced successfully
	MessageResourceSynced = "Addon synced successfully"

	// maxRetries is the number of times a deployment will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the times
	// a deployment is going to be requeued:
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15
)

type Controller struct {
	Owner string
	// kubeclientset is a standard kubernetes clientset
	kubeclientset kubernetes.Interface
	// sampleclientset is a clientset for our own API group
	addonclientset clientset.Interface

	addonLister addonlisters.AddonLister
	addonSynced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder

	config *restclient.Config
}

func NewController(
	kubeclientset kubernetes.Interface,
	addonclientset clientset.Interface,
	addonInformer informers.AddonInformer,
	config *restclient.Config) *Controller {

	// Create event broadcaster
	// Add addon-controller types to the default Kubernetes Scheme so Events can be
	// logged for addon-controller types.
	utilruntime.Must(addonscheme.AddToScheme(scheme.Scheme))
	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})
	controller := &Controller{
		kubeclientset:  kubeclientset,
		addonclientset: addonclientset,
		addonLister:    addonInformer.Lister(),
		addonSynced:    addonInformer.Informer().HasSynced,
		workqueue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "addons"),
		recorder:       recorder,
		config:         config,
	}
	// set up an event handler for when addon resources change.
	addonInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueAddon,
		UpdateFunc: func(oldObj, newObj interface{}) {
			controller.enqueueAddon(newObj)
		},
		DeleteFunc: controller.deleteAddon,
	})
	return controller
}

func (c *Controller) deleteAddon(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	klog.V(4).Infof("Deleting %s ", key)
	c.enqueueAddon(obj)
}

func (c *Controller) enqueueAddon(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.Add(key)
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Foo resource to be synced.
		if err := c.syncHandler(key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		return nil
	}(obj)

	//c.handError(err,obj)
	if err != nil {
		utilruntime.HandleError(err)
	}

	return true
}

func (c *Controller) handError(err error, key interface{}) {
	if err != nil {
		c.workqueue.Forget(key)
		return
	}
	if c.workqueue.NumRequeues(key) < maxRetries {
		klog.V(2).Infof("Error syncing addon %v: %v", key, err)
		c.workqueue.AddRateLimited(key)
		return
	}
	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping addon %q out of the queue: %v", key, err)
	c.workqueue.Forget(key)

}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the Foo resource
// with the current status of the resource.
func (c *Controller) syncHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	ctx := context.TODO()
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}
	//check namespace exist,if not create it.
	_, err = c.kubeclientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			ns := GetNamespace(namespace)
			_, err = c.kubeclientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		} else {
			utilruntime.HandleError(err)
			return err
		}
	}
	if err != nil {
		utilruntime.HandleError(err)
		return err
	}
	// build helm cli
	cli, err := helm.NewHelmCli(c.config, namespace)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Can not build helm control.%s", err.Error()))
		return err
	}
	// Get the Addon resource with this namespace/name
	addon, err := c.addonLister.Addons(namespace).Get(name)
	if err != nil {
		// The addon resource may no longer exist, in which case we stop
		// processing  and uninstall release.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("addon '%s' in work queue no longer exists and uninstalling addon", key))
			_, err := cli.Uninstall(name, namespace)
			// uninstall fail (except not found) continue.
			if err != nil && !strings.Contains(err.Error(), NotFound) {
				klog.Errorf("uninstall %s namespace %s error %s:", name, namespace, err)
				return err
			}
			return nil
		}
		return err
	}

	addonTemplateName := addon.Spec.AddonTemplateName
	if addonTemplateName == "" {
		// We choose to absorb the error here as the worker would requeue the
		// resource otherwise. Instead, the next time the resource is updated
		// the resource will be queued again.
		utilruntime.HandleError(fmt.Errorf("%s: addon name must be specified", key))
		return nil
	}
	// Get the release with the name specified in addon.name
	status := addonv1.Unknown
	res, err := cli.Get(name)
	// If the release doesn't exist, we'll create it
	if err != nil && strings.Contains(err.Error(), NotFound) {
		// create it
		res, err = installAddon(cli, addon)
		if err != nil {
			c.recorder.Event(addon, corev1.EventTypeWarning, ErrorInstallFail, err.Error())
			status = addonv1.Failed
			reason := fmt.Sprintf("Install addon %s error:%s", addon.Name, err.Error())
			err = c.syncAddonErrorStatus(addon, status, reason)
			return err
		}
		status = addonv1.Installing
	}
	// If an error occurs during Get/Create, we'll requeue the item so we can
	// attempt processing again later. This could have been caused by a
	// temporary network failure, or any other transient reason.
	if err != nil {
		err = c.syncAddonErrorStatus(addon, addonv1.Unavailable, fmt.Sprintf("Get Addon error %s", err.Error()))
		return err
	}
	// check addon version, if is installing  skip!
	if status != addonv1.Installing && c.checkAddonVersion(addon, res) {
		res, err = upgradeAddon(cli, addon)
		if err != nil {
			c.recorder.Event(addon, corev1.EventTypeWarning, ErrorUpgradeFail, err.Error())
			status = addonv1.Failed
			err = c.syncAddonErrorStatus(addon, addonv1.Unavailable, fmt.Sprintf("Upgrade Addon error %s", err.Error()))
			return err
		}
		status = addonv1.Upgrading
	}

	// Finally, we update the status block of the Foo resource to reflect the
	// current state of the world
	klog.V(4).Infof("Health check %s %s", addon.Name, addon.Namespace)
	addonDeepCopy := c.healthCheckAndBuildStatus(addon, res, status)
	err = c.updateAddonStatus(addon, addonDeepCopy)
	if err != nil {
		return err
	}
	return nil
}

func (c *Controller) handleErrorAndUpdateStatus(addon *addonv1.Addon, err error) error {
	addon.Status.Reason = err.Error()
	_, err = c.addonclientset.LstackV1().Addons(addon.Namespace).Update(context.TODO(), addon, metav1.UpdateOptions{})
	return err
}

func (c *Controller) syncAddonErrorStatus(addon *addonv1.Addon, status addonv1.Status, reason string) error {
	deepCopy := addon.DeepCopy()
	deepCopy.Status.Status = status
	deepCopy.Status.Reason = reason
	return c.updateAddonStatus(addon, deepCopy)
}

func (c *Controller) updateAddonStatus(addon *addonv1.Addon, newAddon *addonv1.Addon) error {
	// NEVER modify objects from the store. It's a read-only, local cache.
	// You can use DeepCopy() to make a deep copy of original object and modify this copy
	// Or create a copy manually for better performance
	if !reflect.DeepEqual(addon.Status, newAddon.Status) {
		_, err := c.addonclientset.LstackV1().Addons(addon.Namespace).UpdateStatus(context.TODO(), newAddon, metav1.UpdateOptions{})
		return err
	}
	return nil
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting addon controller")

	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.addonSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	// Launch two workers to process Foo resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

//Verify the version of the addon to determine if an upgrade is required
func (c *Controller) checkAddonVersion(addon *addonv1.Addon, release *release.Release) bool {
	return release.Chart.Metadata.Version != addon.Spec.Version || !compareValues(addon.Spec.Values, addon.Status.CurrentValues)
}

func compareValues(new string, cur string) bool {
	if new == "" || cur == "" {
		return false
	}
	newMap := map[string]interface{}{}
	curMap := map[string]interface{}{}
	err := json.Unmarshal([]byte(new), &newMap)
	if err != nil {
		klog.Errorf(err.Error())
		return true
	}
	err = json.Unmarshal([]byte(cur), &curMap)
	if err != nil {
		klog.Errorf("")
		return true
	}
	res := reflect.DeepEqual(newMap, curMap)
	return res
}

func installAddon(cli *helm.HelmCli, addon *addonv1.Addon) (*release.Release, error) {
	klog.V(4).Infof("Start install addon %s namespace: %s, chart address is %s,values %s",
		addon.Name, addon.Namespace, addon.Spec.ChartAddr, addon.Spec.Values)
	chart, err := helm.GetChart(addon.Spec.ChartAddr)
	if err != nil {
		return nil, err
	}
	val := map[string]interface{}{}
	err = json.Unmarshal([]byte(addon.Spec.Values), &val)
	if err != nil {
		return nil, err
	}
	res, err := cli.Install(addon.Name, addon.Namespace, val, chart)
	if err != nil {
		return nil, err
	}
	return res, err
}

func upgradeAddon(cli *helm.HelmCli, addon *addonv1.Addon) (*release.Release, error) {
	klog.V(4).Infof("Starting upgrade addon %s namespace: %s, chart address is %s", addon.Name, addon.Namespace, addon.Spec.ChartAddr)
	chart, err := helm.GetChart(addon.Spec.ChartAddr)
	if err != nil {
		return nil, err
	}
	val := map[string]interface{}{}
	err = json.Unmarshal([]byte(addon.Spec.Values), &val)
	if err != nil {
		return nil, err
	}
	res, err := cli.Upgrade(addon.Name, addon.Namespace, val, chart)
	if err != nil {
		return nil, err
	}
	return res, err
}

func (c *Controller) healthCheckAndBuildStatus(addon *addonv1.Addon, res *release.Release, currentStatus addonv1.Status) *addonv1.Addon {
	inOperation := false
	sub := time.Now().Sub(res.Info.LastDeployed.Time)
	// addon 资源status 显示为安装中或升级中  且时间不超过10分钟
	if (addon.Status.Status == addonv1.Installing || addon.Status.Status == addonv1.Upgrading) && sub.Minutes() <= 10 {
		inOperation = true
	}
	addonCopy := addon.DeepCopy()
	addonCopy.Status.CurrentValues = addon.Spec.Values
	addonCopy.Status.CurrentVersion = addon.Spec.Version
	if addon.Spec.ReadinessProbe != nil && len(addon.Spec.ReadinessProbe.Targets) > 0 {
		var status addonv1.Status
		var reason string
		for _, v := range addon.Spec.ReadinessProbe.Targets {
			switch v.Resource {
			case "Deployment":
				status = c.getDeploymentStatus(v.Name, addon)
			case "StatefulSet":
				status = c.getStatefulSetStatus(v.Name, addon)
			case "DaemonSet":
				status = c.getDaemonSetStatus(v.Name, addon)
			default:
				status = addonv1.Unknown
				reason = fmt.Sprintf("Unsupport readiness probe resource %s", v.Resource)
			}
			// only Available break
			if status != addonv1.Available {
				break
			}
		}
		if reason != "" {
			addonCopy.Status.Reason = reason
		}
		// 若资源正则操作中 且不可用 则直接返回
		if inOperation && status != addonv1.Available {

			return addonCopy
		}
		if currentStatus == addonv1.Installing || currentStatus == addonv1.Upgrading {
			addonCopy.Status.Status = currentStatus
			return addonCopy
		}
		if status != addonv1.Available {
			addonCopy.Status.Status = status
			return addonCopy
		}

	}
	addonCopy.Status.Status = addonv1.Available
	addonCopy.Status.Reason = ""
	addonCopy.Status.Message = ""
	return addonCopy
}

func (c *Controller) getDeploymentStatus(name string, addon *addonv1.Addon) addonv1.Status {
	res, err := c.kubeclientset.AppsV1().Deployments(addon.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("get deploy %s error: %s", name, err.Error())
		return addonv1.Unavailable
	}
	msg := fmt.Sprintf("workload Deployment %s/%s found %d require %d", addon.Namespace, name, res.Status.AvailableReplicas, res.Status.Replicas)
	klog.Infof(msg)
	if res.Status.Replicas > 0 && res.Status.ReadyReplicas > 0 && res.Status.AvailableReplicas > 0 {
		return addonv1.Available
	}
	c.recorder.Event(addon, corev1.EventTypeWarning, ErrorHealthCheckFail, msg)
	return addonv1.Unavailable
}

func (c *Controller) getStatefulSetStatus(name string, addon *addonv1.Addon) addonv1.Status {

	res, err := c.kubeclientset.AppsV1().StatefulSets(addon.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Get statefulSet %s error: %s", name, err.Error())
		return addonv1.Unavailable
	}
	msg := fmt.Sprintf("workload StatefulSet %s/%s found %d require %d", addon.Namespace, name, res.Status.ReadyReplicas, res.Status.Replicas)
	klog.Infof(msg)
	if res.Status.Replicas > 0 && res.Status.ReadyReplicas > 0 {
		return addonv1.Available
	}
	c.recorder.Event(addon, corev1.EventTypeWarning, ErrorHealthCheckFail, msg)
	return addonv1.Unavailable
}

func (c *Controller) getDaemonSetStatus(name string, addon *addonv1.Addon) addonv1.Status {

	res, err := c.kubeclientset.AppsV1().DaemonSets(addon.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Get statefulSet %s error: %s", name, err.Error())
		return addonv1.Unavailable
	}
	msg := fmt.Sprintf("workload DaemonSet %s/%s ready %d available %d", addon.Namespace, name, res.Status.NumberReady, res.Status.NumberAvailable)
	klog.Infof(msg)
	if res.Status.NumberReady > 0 && res.Status.NumberAvailable > 0 {
		return addonv1.Available
	}
	c.recorder.Event(addon, corev1.EventTypeWarning, ErrorHealthCheckFail, msg)
	return addonv1.Unavailable
}

func GetNamespace(name string) *corev1.Namespace {
	ns := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	return ns

}
