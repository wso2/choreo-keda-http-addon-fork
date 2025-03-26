package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func getHost(r *http.Request, reverseDNSRetry int, reverseDNSRetryInternal time.Duration) (string, error) {
	remoteIP := r.RemoteAddr
	if remoteIP == "" {
		return "", fmt.Errorf("remote address not found")
	}
	// removing port if exists
	if i := strings.Index(remoteIP, ":"); i != -1 {
		remoteIP = remoteIP[:i]
	}

	host := r.Host
	if host == "" {
		return "", fmt.Errorf("host not found")
	}
	// removing port if exists
	hostPort := ""
	if i := strings.Index(host, ":"); i != -1 {
		host = host[:i]
		hostPort = host[i:]
	}

	// ReverseDNS lookup on the remote IP
	var names []string
	var err error

	for i := 0; i < reverseDNSRetry; i++ {
		names, err = net.LookupAddr(remoteIP)
		if err == nil && len(names) > 0 {
			break
		}
		time.Sleep(reverseDNSRetryInternal)
	}
	if err != nil {
		return "", fmt.Errorf("error looking up address %q: %s", remoteIP, err)
	}

	if len(names) == 0 {
		return "", fmt.Errorf("no names found for address %q", remoteIP)
	}
	remoteDNS := names[0]
	_, _, remoteNs := extractPodInfo(remoteDNS)
	if remoteNs == "" {
		return "", fmt.Errorf("namespace not found in %q", remoteDNS)
	}

	// Extracting service name and namespace from the host header
	destService, destNs := extractServiceInfo(host)

	// If the caller is from user namespace or
	// the destination namespace is not provided in the host header
	// then the destination namespace is the same as the caller namespace
	if strings.HasPrefix(remoteNs, "dp-") || destNs == "" {
		host = fmt.Sprintf("%s.%s%s", destService, remoteNs, hostPort)
	} else {
		host = fmt.Sprintf("%s.%s%s", destService, destNs, hostPort)
	}

	return host, nil
}

// $SVC.$NAMESPACE.svc.cluster.local
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

// $POD_IP.$DEPLOYMENT_NAME.$NAMESPACE.svc.cluster.local
func extractPodInfo(podURL string) (string, string, string) {
	parts := strings.Split(podURL, ".")

	if len(parts) >= 3 {
		podIP := parts[0]
		deploymentName := parts[1]
		namespace := parts[2]
		return podIP, deploymentName, namespace
	}

	if len(parts) == 2 {
		podIP := parts[0]
		deploymentName := parts[1]
		return podIP, deploymentName, ""
	}

	return "", "", ""
}

// getPort returns the port from the request host header
// if it exists
func getPort(r *http.Request) *int32 {
	host := r.Host
	_, portStr, err := net.SplitHostPort(host)
	if err != nil {
		// If there's no explicit port, return nil
		return nil
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil
	}

	portInt32 := int32(port)
	return &portInt32
}
