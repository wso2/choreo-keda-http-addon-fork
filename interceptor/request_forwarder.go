package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.WriteHeader(502)
		errMsg := fmt.Errorf("error on backend (%w)", err).Error()
		if _, err := w.Write([]byte(errMsg)); err != nil {
			lggr.Error(err, "could not write error response to client")
		}
	}

	for i := 0; i < maxRetries; i++ {
		responseRecorder := httptest.NewRecorder()
		proxy.ServeHTTP(responseRecorder, r)
		response := responseRecorder.Result()

		if response.StatusCode == http.StatusServiceUnavailable {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			lggr.Info("Received 503 from upstream", "body", string(body))
			if strings.HasPrefix(string(body), "upstream connect error or disconnect/reset before headers") {
				if i < maxRetries-1 {
					lggr.Info("Retrying request due to upstream error", "attempt", i+1)
					time.Sleep(time.Second * time.Duration(2*(i+1)))
					continue
				} else {
					lggr.Error(nil, "Max retries reached, returning last response")
				}
			}
			w.WriteHeader(response.StatusCode)
			w.Write(body)
			return
		}

		// If the response is not 503, write it and return
		response.Write(w)
		return
	}
}
