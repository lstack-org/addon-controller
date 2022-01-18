package helm

import (
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// helm 操作接口
type HelmInterface interface {
	// 安装
	Install(release, namespace string, values map[string]interface{}, chart *chart.Chart) (*release.Release, error)
	// 列表
	List(namespace string) ([]*release.Release, error)
	// 卸载  注： 不会卸载PVC  CRD  资源
	Uninstall(releaseName, namespace string) (string, error)
	// 升级
	Upgrade(releaseName, namespace string, values map[string]interface{}, chart *chart.Chart) (*release.Release, error)

	Test(releaseName, namespace string) error

	Get(name string) (*release.Release, error)
}

type HelmCli struct {
	ActionConfig *action.Configuration
}

var _ HelmInterface = &HelmCli{}

//新建helm cli
func NewHelmCli(config *rest.Config, namespace string) (*HelmCli, error) {
	configuration, err := GetActionConfig(config, namespace)
	if err != nil {
		return nil, err
	}
	return &HelmCli{ActionConfig: configuration}, nil
}

func (hc *HelmCli) Install(release, namespace string, values map[string]interface{}, chart *chart.Chart) (*release.Release, error) {

	installCli := action.NewInstall(hc.ActionConfig)
	installCli.CreateNamespace = true
	installCli.Namespace = namespace
	installCli.ReleaseName = release
	resRelease, err := installCli.Run(chart, values)
	if err != nil {
		return nil, err
	}

	return resRelease, nil
}

func (hc *HelmCli) Get(name string) (*release.Release, error) {
	cli := action.NewGet(hc.ActionConfig)
	return cli.Run(name)

}

func (hc *HelmCli) List(namespace string) ([]*release.Release, error) {
	rlCli := action.NewList(hc.ActionConfig)
	if namespace == "" {
		rlCli.AllNamespaces = true
		rlCli.All = true
	}
	releases, err := rlCli.Run()
	if err != nil {
		return nil, err
	}
	return releases, nil

}

func (hc *HelmCli) Uninstall(releaseName, namespace string) (string, error) {
	uninstallCli := action.NewUninstall(hc.ActionConfig)
	uninstallResponse, err := uninstallCli.Run(releaseName)
	if err != nil {
		return "", err
	}
	return uninstallResponse.Info, nil
}
func (hc *HelmCli) Upgrade(releaseName, namespace string, values map[string]interface{}, chart *chart.Chart) (*release.Release, error) {
	upgradeCli := action.NewUpgrade(hc.ActionConfig)
	upgradeCli.Namespace = namespace
	res, err := upgradeCli.Run(releaseName, chart, values)
	if err != nil {
		klog.Error(err.Error())
		return nil, err
	}
	return res, nil
}

func (hc *HelmCli) Test(releaseName, namespace string) error {
	testCli := action.NewReleaseTesting(hc.ActionConfig)
	testCli.Namespace = namespace
	_, err := testCli.Run(releaseName)
	if err != nil {
		klog.Error(err.Error())
		return err
	}
	return nil
}
