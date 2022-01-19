package main

import (
	"flag"
	"github.com/lstack-org/addon-controller/app"
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
	app.Run(kubeconfig, time.Duration(refresh))
}
