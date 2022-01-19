package app

import (
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	apiserver "k8s.io/apiserver/pkg/server"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	"k8s.io/apiserver/pkg/server/mux"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/configz"
	"k8s.io/component-base/metrics/legacyregistry"
	_ "k8s.io/component-base/metrics/prometheus/workqueue" // for workqueue metric registration
	"net/http"
)

// BuildHandlerChain builds a handler chain with a base handler and CompletedConfig.
func BuildHandlerChain(apiHandler http.Handler, authorizationInfo *apiserver.AuthorizationInfo, authenticationInfo *apiserver.AuthenticationInfo) http.Handler {
	requestInfoResolver := &apirequest.RequestInfoFactory{}
	failedHandler := genericapifilters.Unauthorized(scheme.Codecs)

	handler := apiHandler
	if authorizationInfo != nil {
		handler = genericapifilters.WithAuthorization(apiHandler, authorizationInfo.Authorizer, scheme.Codecs)
	}
	if authenticationInfo != nil {
		handler = genericapifilters.WithAuthentication(handler, authenticationInfo.Authenticator, failedHandler, nil)
	}
	handler = genericapifilters.WithRequestInfo(handler, requestInfoResolver)
	handler = genericapifilters.WithCacheControl(handler)

	handler = genericfilters.WithPanicRecovery(handler, requestInfoResolver)

	return handler
}

// NewBaseHandler takes in CompletedConfig and returns a handler.
func NewBaseHandler(healthzHandler http.Handler) *mux.PathRecorderMux {
	mux := mux.NewPathRecorderMux("controller-manager")
	mux.Handle("/healthz", healthzHandler)
	configz.InstallHandler(mux)
	//lint:ignore SA1019 See the Metrics Stability Migration KEP
	mux.Handle("/metrics", legacyregistry.Handler())

	return mux
}
