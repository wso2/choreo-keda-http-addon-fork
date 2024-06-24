package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strings"
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
	if host == "" {
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
		host = fmt.Sprintf("%s.%s", destService, remoteNs)
	} else {
		host = fmt.Sprintf("%s.%s", destService, destNs)
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
