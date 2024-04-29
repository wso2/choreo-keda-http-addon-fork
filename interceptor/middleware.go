package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"github.com/kedacore/http-add-on/pkg/queue"
)

func getHost(r *http.Request) (string, error) {
	remoteIP := r.RemoteAddr
	if remoteIP == "" {
		return "", fmt.Errorf("remote address not found")
	}
	// removing port if exists
	if i := strings.Index(remoteIP, ":"); i != -1 {
		remoteIP = remoteIP[:i]
	}

	host := r.Host
	if host != "" {
		return "", fmt.Errorf("host not found")
	}
	// removing port if exists
	if i := strings.Index(host, ":"); i != -1 {
		host = host[:i]
	}

	// ReverseDNS lookup on the remote IP
	names, err := net.LookupAddr(remoteIP)
	if err != nil {
		return "", fmt.Errorf("error looking up address %q: %s", remoteIP, err)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no names found for address %q", remoteIP)
	}
	remoteDNS := names[0]
	_, remoteNs := extractServiceInfo(remoteDNS)
	if remoteNs == "" {
		return "", fmt.Errorf("namespace not found in %q", remoteDNS)
	}

	// Extracting service name and namespace from the host header
	destService, destNs := extractServiceInfo(host)

	// If the caller is from user namespace or
	// the destination namespace is not provided in the host header
	// then the destination namespace is the same as the caller namespace
	if strings.HasPrefix(remoteNs, "dp-") || destNs == "" {
		host = fmt.Sprintf("%s.%s.svc.cluster.local", destService, remoteNs)
		return host, nil
	}

	host = fmt.Sprintf("%s.%s.svc.cluster.local", destService, destNs)
	return host, nil
}

// $SVC.$NAMESPACE.svc.cluster.local
// $POD.$NAMESPACE.pod.cluster.local
func extractServiceInfo(serviceURL string) (string, string) {
	parts := strings.Split(serviceURL, ".")

	if len(parts) >= 2 {
		serviceName := parts[0]
		namespace := parts[1]
		return serviceName, namespace
	}

	if len(parts) == 1 {
		serviceName := parts[0]
		return serviceName, ""
	}

	return "", ""
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
			if q.ShouldPostponeResize() && q.Count(host) == 1 {
				lggr.Info("postponing resize", "host", host)
				q.PostponeResize(host, time.Now().Add(q.PostponeDuration()))
				return
			}

			if err := q.Resize(host, -1); err != nil {
				log.Printf("Error decrementing queue for %q (%s)", r.RequestURI, err)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
