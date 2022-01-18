package helm

import (
	"fmt"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"k8s.io/klog/v2"
	"net/http"
)

func GetChart(addonAddr string) (*chart.Chart, error) {
	response, err := request(addonAddr)
	if err != nil {
		klog.Errorf("Get chart error:%s", err)
		return nil, err
	}
	raw := response.Body
	defer raw.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		chartFile, err := loader.LoadArchive(raw)
		if err != nil {
			klog.Errorf("Load chart error:%s", err)
			return nil, err
		}
		return chartFile, nil
	} else {
		return nil, fmt.Errorf("Can't find chart!, path is %s,httpcode:%d", addonAddr, response.StatusCode)
	}

}

var client = &http.Client{}

func request(url string) (*http.Response, error) {
	newRequest, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(newRequest)
}
