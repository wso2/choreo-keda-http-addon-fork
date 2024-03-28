package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-logr/logr"

	"github.com/kedacore/http-add-on/pkg/queue"
)

func getHost(r *http.Request) (string, error) {
	host := r.Host
	if host != "" {
		// removing port if exists
		if i := strings.Index(host, ":"); i != -1 {
			host = host[:i]
		}
		// handling the case where host contains only the service name (without the namespace)
		if i := strings.Index(host, "."); i == -1 {
			choreoProjectNs := r.Header.Get("X-Choreo-Project-Ns")
			if choreoProjectNs == "" {
				return "", fmt.Errorf("X-Choreo-Project-Ns header was not found in the service to service request")
			}
			host = host + "." + choreoProjectNs
		}
		// removing svc.cluster.local
		// check for suffix svc.cluster.local in host string
		if strings.HasSuffix(host, ".svc.cluster.local") {
			// remove suffix from the host
			host = strings.TrimSuffix(host, ".svc.cluster.local")
		}
		return host, nil
	}
	return "", fmt.Errorf("host not found")
}

// countMiddleware adds 1 to the given queue counter, executes next
// (by calling ServeHTTP on it), then decrements the queue counter
func countMiddleware(
	lggr logr.Logger,
	q queue.Counter,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, err := getHost(r)
		if err != nil {
			lggr.Error(err, "not forwarding request")
			w.WriteHeader(400)
			if _, err := w.Write([]byte("Host not found, not forwarding request")); err != nil {
				lggr.Error(err, "could not write error message to client")
			}
			return
		}
		lggr.Info("request received.", "host", host)
		if err := q.Resize(host, +1); err != nil {
			log.Printf("Error incrementing queue for %q (%s)", r.RequestURI, err)
		}
		defer func() {
			if err := q.Resize(host, -1); err != nil {
				log.Printf("Error decrementing queue for %q (%s)", r.RequestURI, err)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
