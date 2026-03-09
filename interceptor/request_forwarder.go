package main

import (
	"bytes"
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
		// Capture all X-* headers from the original request before
		// modifying the outgoing request, so they are preserved
		// through the proxy and not lost or overwritten.
		xHeaders := make(http.Header)
		for key, values := range r.Header {
			if strings.HasPrefix(strings.ToUpper(key), "X-") {
				xHeaders[key] = values
			}
		}
		req.URL = fwdSvcURL
		req.Host = fwdSvcURL.Host
		req.URL.Path = r.URL.Path
		req.URL.RawQuery = r.URL.RawQuery
		req.Header.Del("X-Forwarded-For")
		// Restore all original X-* headers (except X-Forwarded-For)
		for key, values := range xHeaders {
			if !strings.EqualFold(key, "X-Forwarded-For") {
				req.Header[key] = values
			}
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.WriteHeader(502)
		errMsg := fmt.Errorf("error on backend (%w)", err).Error()
		if _, err := w.Write([]byte(errMsg)); err != nil {
			lggr.Error(
				err,
				"could not write error response to client",
			)
		}
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode == http.StatusServiceUnavailable {
			buf := new(bytes.Buffer)
			_, err := buf.ReadFrom(resp.Body)
			if err != nil {
				return err
			}
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))

			// Check if the response body starts with "upstream connect error or disconnect/reset before headers"
			if bytes.HasPrefix(buf.Bytes(), []byte("upstream connect error or disconnect/reset before headers")) {
				updatedResp, err := retryRequest(lggr, resp, maxRetries, 0)
				if err != nil {
					return err
				}
				*resp = *updatedResp
			}
		}
		return nil
	}
	proxy.ServeHTTP(w, r)
}

func retryRequest(lggr logr.Logger, resp *http.Response, maxRetries, attempt int) (*http.Response, error) {
	if attempt >= maxRetries {
		lggr.Error(nil, "Max retries reached, returning last response")
		return resp, nil
	}

	lggr.Info("Service unavailable, retrying", "attempt", attempt+1)
	time.Sleep(time.Second * time.Duration(2*(attempt+1)))

	newResp, err := http.DefaultTransport.RoundTrip(resp.Request)
	if err != nil {
		return retryRequest(lggr, resp, maxRetries, attempt+1)
	}

	if newResp.StatusCode == http.StatusServiceUnavailable {
		buf := new(bytes.Buffer)
		_, err := buf.ReadFrom(newResp.Body)
		if err != nil {
			return nil, err
		}
		newResp.Body.Close()
		newResp.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))

		// Check if the response body starts with "upstream connect error or disconnect/reset before headers"
		if bytes.HasPrefix(buf.Bytes(), []byte("upstream connect error or disconnect/reset before headers")) {
			return retryRequest(lggr, newResp, maxRetries, attempt+1)
		}
	}

	return newResp, nil
}
