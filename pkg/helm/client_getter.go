package helm

import (
	"helm.sh/helm/v3/pkg/action"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	diskcached "k8s.io/client-go/discovery/cached/disk"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/klog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var defaultCacheDir = filepath.Join(homedir.HomeDir(), ".kube", "http-cache")

type HelmRESTClientGetter struct {
	config    *rest.Config
	Namespace string
}

func GetActionConfig(config *rest.Config, namespace string) (*action.Configuration, error) {
	getter := &HelmRESTClientGetter{config: config, Namespace: namespace}
	actionConfiguration := new(action.Configuration)
	if err := actionConfiguration.Init(getter, namespace, os.Getenv("HELM_DRIVER"), debug); err != nil {
		klog.Errorf("Get helm configuration error:%s", err)
		return nil, err
	}
	return actionConfiguration, nil

}

// ToRESTConfig returns restconfig
func (hc *HelmRESTClientGetter) ToRESTConfig() (*rest.Config, error) {
	return hc.config, nil
}

// ToDiscoveryClient returns discovery client
func (hc *HelmRESTClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	config, err := hc.ToRESTConfig()
	if err != nil {
		return nil, err
	}

	// The more groups you have, the more discovery requests you need to make.
	// given 25 groups (our groups + a few custom resources) with one-ish version each, discovery needs to make 50 requests
	// double it just so we don't end up here again for a while.  This config is only used for discovery.
	config.Burst = 100

	// retrieve a user-provided value for the "cache-dir"
	// defaulting to ~/.kube/http-cache if no user-value is given.
	httpCacheDir := defaultCacheDir
	discoveryCacheDir := computeDiscoverCacheDir(filepath.Join(homedir.HomeDir(), ".kube", "cache", "discovery"), config.Host)
	return diskcached.NewCachedDiscoveryClientForConfig(config, discoveryCacheDir, httpCacheDir, time.Duration(10*time.Minute))

}

// ToRESTMapper returns a restmapper
func (hc *HelmRESTClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	discoveryClient, err := hc.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
	expander := restmapper.NewShortcutExpander(mapper, discoveryClient)
	return expander, nil
}

// ToRawKubeConfigLoader return kubeconfig loader as-is
func (hc *HelmRESTClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	//klog.Warningf("Use ToRawKubeConfigLoader unknown error may result!!")
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.DefaultClientConfig = &clientcmd.DefaultClientConfig
	overrides := &clientcmd.ConfigOverrides{ClusterDefaults: clientcmd.ClusterDefaults}
	if hc.config.BearerToken != "" {
		overrides.AuthInfo.Token = hc.config.BearerToken
	}
	overrides.Context.Namespace = hc.Namespace
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	return clientConfig
}

func computeDiscoverCacheDir(parentDir, host string) string {
	// strip the optional scheme from host if its there:
	schemelessHost := strings.Replace(strings.Replace(host, "https://", "", 1), "http://", "", 1)
	// now do a simple collapse of non-AZ09 characters.  Collisions are possible but unlikely.  Even if we do collide the problem is short lived
	safeHost := overlyCautiousIllegalFileCharacters.ReplaceAllString(schemelessHost, "_")
	return filepath.Join(parentDir, safeHost)
}

// overlyCautiousIllegalFileCharacters matches characters that *might* not be supported.  Windows is really restrictive, so this is really restrictive
var overlyCautiousIllegalFileCharacters = regexp.MustCompile(`[^(\w/\.)]`)

func debug(format string, v ...interface{}) {

}
