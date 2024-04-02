package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
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
	proxy := httputil.NewSingleHostReverseProxy(fwdSvcURL)
	proxy.Transport = roundTripper
	proxy.Director = func(req *http.Request) {
		req.URL = fwdSvcURL
		req.Host = fwdSvcURL.Host
		req.URL.Path = r.URL.Path
		req.URL.RawQuery = r.URL.RawQuery
		req.Header.Del("X-Forwarded-For")
	}

	var lastResponse *http.Response
	proxy.ModifyResponse = func(resp *http.Response) error {
		lastResponse = resp
		return nil
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

	for i := 0; i < maxRetries; i++ {
		proxy.ServeHTTP(w, r)

		if lastResponse != nil && lastResponse.StatusCode == http.StatusServiceUnavailable {
			body, _ := io.ReadAll(lastResponse.Body)
			lastResponse.Body.Close()

			if strings.HasPrefix(string(body), "upstream connect error or disconnect/reset before headers") {
				if i < maxRetries-1 {
					lggr.Info("Retrying request due to upstream error", "attempt", i+1)
					time.Sleep(time.Second * time.Duration(2*(i+1)))
					continue
				} else {
					lggr.Error(nil, "Max retries reached, returning last response")
				}
			}
			w.WriteHeader(lastResponse.StatusCode)
			w.Write(body)
			return
		}
		return
	}
}
