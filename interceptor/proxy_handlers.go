package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-logr/logr"

	"github.com/kedacore/http-add-on/interceptor/config"
	"github.com/kedacore/http-add-on/interceptor/handler"
	kedanet "github.com/kedacore/http-add-on/pkg/net"
	"github.com/kedacore/http-add-on/pkg/util"
)

type forwardingConfig struct {
	waitTimeout             time.Duration
	respHeaderTimeout       time.Duration
	forceAttemptHTTP2       bool
	maxIdleConns            int
	idleConnTimeout         time.Duration
	tlsHandshakeTimeout     time.Duration
	expectContinueTimeout   time.Duration
	serviceUnavailableRetry int
}

func newForwardingConfigFromTimeouts(t *config.Timeouts) forwardingConfig {
	return forwardingConfig{
		waitTimeout:             t.WorkloadReplicas,
		respHeaderTimeout:       t.ResponseHeader,
		forceAttemptHTTP2:       t.ForceHTTP2,
		maxIdleConns:            t.MaxIdleConns,
		idleConnTimeout:         t.IdleConnTimeout,
		tlsHandshakeTimeout:     t.TLSHandshakeTimeout,
		expectContinueTimeout:   t.ExpectContinueTimeout,
		serviceUnavailableRetry: t.ServiceUnavailableRetry,
	}
}

// newForwardingHandler takes in the service URL for the app backend
// and forwards incoming requests to it. Note that it isn't multitenant.
// It's intended to be deployed and scaled alongside the application itself.
//
// fwdSvcURL must have a valid scheme in it. The best way to do this is
// creating a URL with url.Parse("https://...")
func newForwardingHandler(
	lggr logr.Logger,
	dialCtxFunc kedanet.DialContextFunc,
	waitFunc forwardWaitFunc,
	fwdCfg forwardingConfig,
	tlsCfg *tls.Config,
) http.Handler {
	roundTripper := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialCtxFunc,
		ForceAttemptHTTP2:     fwdCfg.forceAttemptHTTP2,
		MaxIdleConns:          fwdCfg.maxIdleConns,
		IdleConnTimeout:       fwdCfg.idleConnTimeout,
		TLSHandshakeTimeout:   fwdCfg.tlsHandshakeTimeout,
		ExpectContinueTimeout: fwdCfg.expectContinueTimeout,
		ResponseHeaderTimeout: fwdCfg.respHeaderTimeout,
		TLSClientConfig:       tlsCfg,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		httpso := util.HTTPSOFromContext(ctx)

		waitFuncCtx, done := context.WithTimeout(r.Context(), fwdCfg.waitTimeout)
		defer done()
		isColdStart, err := waitFunc(
			waitFuncCtx,
			httpso.GetNamespace(),
			httpso.Spec.ScaleTargetRef.Service,
		)
		if err != nil {
			lggr.Error(err, "wait function failed, not forwarding request")
			w.WriteHeader(http.StatusBadGateway)
			if _, err := w.Write([]byte(fmt.Sprintf("error on backend (%s)", err))); err != nil {
				lggr.Error(err, "could not write error response to client")
			}
			return
		}
		w.Header().Add("X-KEDA-HTTP-Cold-Start", strconv.FormatBool(isColdStart))

		uh := handler.NewUpstream(roundTripper, fwdCfg.serviceUnavailableRetry)
		uh.ServeHTTP(w, r)
	})
}
