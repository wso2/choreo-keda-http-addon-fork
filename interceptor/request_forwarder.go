package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/go-logr/logr"
)

func forwardRequest(
	lggr logr.Logger,
	w http.ResponseWriter,
	r *http.Request,
	roundTripper http.RoundTripper,
	fwdSvcURL *url.URL,
	maxRetries int,
) {
	var retryCount int
	var backoffDelay time.Duration

	// add one to the maxRetries because we want to return last error
	// if the service is still down after maxRetries
	for retryCount < maxRetries+1 {
		proxy := httputil.NewSingleHostReverseProxy(fwdSvcURL)
		proxy.Transport = roundTripper
		proxy.Director = func(req *http.Request) {
			req.URL = fwdSvcURL
			req.Host = fwdSvcURL.Host
			req.URL.Path = r.URL.Path
			req.URL.RawQuery = r.URL.RawQuery
			// delete the incoming X-Forwarded-For header so the proxy
			// puts its own in. This is also important to prevent IP spoofing
			req.Header.Del("X-Forwarded-For ")
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			w.WriteHeader(502)
			// note: we can only use the '%w' directive inside of fmt.Errorf,
			// not Sprintf or anything similar. this means we have to create the
			// failure string in this slightly convoluted way.
			errMsg := fmt.Errorf("error on backend (%w)", err).Error()
			if _, err := w.Write([]byte(errMsg)); err != nil {
				lggr.Error(
					err,
					"could not write error response to client",
				)
			}
		}

		resp, err := proxy.Transport.RoundTrip(r)
		if err != nil {
			lggr.Error(err, "error during proxy RoundTrip")
			backoffDelay = time.Duration(retryCount) * time.Second
			time.Sleep(backoffDelay)
			retryCount++
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusServiceUnavailable &&
			resp.Header.Get("x-keda-http-cold-start") == "true" &&
			// we only want to retry if we haven't hit the max number of retries
			retryCount < maxRetries {
			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				lggr.Error(err, "error reading response body")
				break
			}
			if string(bodyBytes) == "upstream connect error or disconnect/reset before headers. retried and the latest reset reason: connection failure, transport failure reason: delayed connect error: 111" {
				backoffDelay = time.Duration(retryCount) * time.Second
				time.Sleep(backoffDelay)
				retryCount++
				continue
			}
		}

		// Forward the response to the client
		proxy.ServeHTTP(w, r)
		return
	}
}
