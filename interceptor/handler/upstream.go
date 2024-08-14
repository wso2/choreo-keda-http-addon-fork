package handler

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/kedacore/http-add-on/pkg/util"
)

var (
	errNilStream = errors.New("context stream is nil")
)

type Upstream struct {
	roundTripper http.RoundTripper
	retryCount   int
}

func NewUpstream(roundTripper http.RoundTripper, retryCount int) *Upstream {
	return &Upstream{
		roundTripper: roundTripper,
		retryCount:   retryCount,
	}
}

var _ http.Handler = (*Upstream)(nil)

func (uh *Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = util.RequestWithLoggerWithName(r, "UpstreamHandler")
	ctx := r.Context()
	logger := util.LoggerFromContext(ctx)

	stream := util.StreamFromContext(ctx)
	if stream == nil {
		sh := NewStatic(http.StatusInternalServerError, errNilStream)
		sh.ServeHTTP(w, r)

		return
	}

	httpso := util.HTTPSOFromContext(ctx)
	if i := strings.Index(r.Host, ":"); i != -1 {
		// if the host header contains port, route to ( routingTarget.Service:port)
		targetPort := r.Host[i+1:]
		targetHost := fmt.Sprintf("http://%s.%s:%s", httpso.Spec.ScaleTargetRef.Service, httpso.GetNamespace(), targetPort)
		var err error
		if stream, err = url.Parse(targetHost); err != nil {
			logger.Error(err, "forwarding failed")
			w.WriteHeader(500)
			if _, err := w.Write([]byte(fmt.Sprintf("error parsing host:port: %s to URL", targetHost))); err != nil {
				logger.Error(err, "could not write error response to client")
			}
			return
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(stream)
	superDirector := proxy.Director
	proxy.Transport = uh.roundTripper
	proxy.Director = func(req *http.Request) {
		superDirector(req)
		req.URL = stream
		req.URL.Path = r.URL.Path
		req.URL.RawPath = r.URL.RawPath
		req.URL.RawQuery = r.URL.RawQuery
		// delete the incoming X-Forwarded-For header so the proxy
		// puts its own in. This is also important to prevent IP spoofing
		req.Header.Del("X-Forwarded-For ")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		sh := NewStatic(http.StatusBadGateway, err)
		sh.ServeHTTP(w, r)
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
				updatedResp, err := retryRequest(logger, resp, uh.retryCount, 0)
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
