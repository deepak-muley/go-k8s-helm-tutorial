package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"sync"

	"github.com/pkg/errors"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/strvals"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/yaml"
)

//https://pkg.go.dev/helm.sh/helm/v3

var (
	// TODO get latest helm so that we dont need to copy following error
	// Copied from https://github.com/helm/helm/blob/master/pkg/storage/driver/driver.go
	// ErrNoDeployedReleases indicates that there are no releases with the given key in the deployed state
	ErrNoDeployedReleases = errors.New("has no deployed releases")

	settings *cli.EnvSettings

	helmLog = ctrl.Log.WithName("helm")
)

type HelmInterface interface {
	InstallChart(name, chartPath, valuesPath, namespace string, args map[string]interface{}) error
	InstallUpgradeChart(name, chartPath, valuesPath, namespace string, args map[string]interface{}) error
	UninstallChart(name, namespace string) error
	ListReleases(namespace, filter string) ([]string, error)
	ReleaseExists(name, namespace string) (bool, error)
}

type HelmClient struct {
	helmMutex sync.Mutex
}

var _ HelmInterface = (*HelmClient)(nil)

// NewHelmClient returns instance pointer
func NewHelmClient() *HelmClient {
	return &HelmClient{}
}

// getHelmActionConfig Helper function to get helm action configuration
func (h *HelmClient) getHelmActionConfig(namespace string) (*action.Configuration, error) {
	h.helmMutex.Lock()
	defer h.helmMutex.Unlock()

	err := os.Setenv("HELM_NAMESPACE", namespace)
	if err != nil {
		helmLog.Error(err,
			"setenv HELM_NAMESPACE failed", "namespace", namespace)
	}
	settings := cli.New()
	cfg := new(action.Configuration)
	err = cfg.Init(
		settings.RESTClientGetter(),
		namespace,
		os.Getenv("HELM_DRIVER"),
		func(format string, args ...interface{}) {
			helmLog.Info(fmt.Sprintf(format, args...))
		})
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// InstallChart
func (h *HelmClient) InstallChart(name, chartPath, valuesPath, namespace string, args map[string]interface{}) error {
	actionConfig, err := h.getHelmActionConfig(namespace)
	if err != nil {
		return err
	}
	// https://github.com/helm/helm/blob/master/pkg/action/install.go
	client := action.NewInstall(actionConfig)

	if client.Version == "" && client.Devel {
		client.Version = ">0.0.0-0"
	}

	client.ReleaseName = name
	chart, err := loader.Load(chartPath)
	if err != nil {
		return err
	}
	vals, err := getValues(valuesPath)
	if err != nil {
		return err
	}

	// Add args
	var setVals interface{}
	if val, ok := args["set"]; ok {
		setVals = val
		if setVals != nil {
			if err := strvals.ParseInto(setVals.(string), vals); err != nil {
				return errors.Wrap(err, "failed parsing --set data")
			}
		}
	}

	client.Namespace = namespace
	// https://github.com/helm/helm/blob/master/pkg/release/release.go
	_, err = client.Run(chart, vals)
	if err != nil {
		return err
	}
	return err
}

func getValues(valsPath string) (map[string]interface{}, error) {
	_, err := os.Stat(valsPath)
	if err != nil {
		return nil, err
	}

	data, err := ioutil.ReadFile(valsPath)
	if err != nil {
		return nil, err
	}

	var mapData map[string]interface{}
	if err = yaml.Unmarshal([]byte(data), &mapData); err != nil {
		return nil, err
	}
	return mapData, nil
}

func (h *HelmClient) InstallUpgradeChart(name, chartPath, valuesPath, namespace string, args map[string]interface{}) error {
	actionConfig, err := h.getHelmActionConfig(namespace)
	if err != nil {
		return err
	}
	// https://github.com/helm/helm/blob/master/pkg/action/install.go
	// https://github.com/fluxcd/helm-operator/blob/master/pkg/helm/options.go
	client := action.NewUpgrade(actionConfig)
	client.Install = true

	chart, err := loader.Load(chartPath)
	if err != nil {
		return err
	}

	vals, err := getValues(valuesPath)
	if err != nil {
		helmLog.Error(err, "getvals failed", "vals", vals)
		return err
	}

	// Add args
	var setVals interface{}
	if val, ok := args["set"]; ok {
		setVals = val
		if setVals != nil {
			if err := strvals.ParseInto(setVals.(string), vals); err != nil {
				return errors.Wrap(err, "failed parsing --set data")
			}
		}
	}

	client.Namespace = namespace
	// https://github.com/helm/helm/blob/master/pkg/release/release.go
	_, err = client.Run(name, chart, vals)
	if err != nil {
		helmLog.Error(err, "Failed to upgrade-install helm chart", "name", name, "namespace", namespace)
		// https://github.com/helm/helm/blob/master/pkg/storage/driver/driver.go
		// var errStr string
		// fmt.Sscanf(errStr, "\"%s\" %s", name, "has no deployed releases")
		// if err == errors.New(errStr) {
		errInstall := h.InstallChart(name, chartPath, valuesPath, namespace, args)
		if errInstall != nil {
			helmLog.Error(err, "Failed to install helm chart", "name", name, "namespace", namespace)
			return errInstall
		} else {
			return nil
		}
	}
	return nil
}

// UninstallChart
func (h *HelmClient) UninstallChart(name, namespace string) error {
	//helm delete $name
	actionConfig, err := h.getHelmActionConfig(namespace)
	if err != nil {
		return err
	}
	client := action.NewUninstall(actionConfig)
	_, err = client.Run(name)
	if err != nil {
		return err
	}
	helmLog.Info("Uninstalled release", "name", name)
	return err
}

func (h *HelmClient) isChartInstallable(ch *chart.Chart) (bool, error) {
	switch ch.Metadata.Type {
	case "", "application":
		return true, nil
	}
	return false, errors.Errorf("%s charts are not installable", ch.Metadata.Type)
}

func (h *HelmClient) ListReleases(namespace, regexFilter string) ([]string, error) {
	var releaseNames []string
	var err error

	actionConfig, err := h.getHelmActionConfig(namespace)
	if err != nil {
		return []string{}, err
	}
	client := action.NewList(actionConfig)
	if len(regexFilter) > 0 {
		client.Filter = regexFilter
	}
	releases, err := client.Run()
	if err != nil {
		return []string{}, err
	}

	for _, release := range releases {
		releaseNames = append(releaseNames, release.Name)
	}

	return releaseNames, nil
}

func (h *HelmClient) ReleaseExists(name, namespace string) (bool, error) {
	releases, err := h.ListReleases(namespace, "")
	if err != nil {
		return false, err
	}

	// TODO did not work
	//if len(releases) != 1 {
	//	return false, nil
	//}

	for _, releaseName := range releases {
		if releaseName == name {
			return true, nil
		}
	}
	return false, nil
}
