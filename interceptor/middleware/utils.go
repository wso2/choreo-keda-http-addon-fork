package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kedacore/http-add-on/pkg/util"
)

// ConnectionInfo holds information about the connection between source and destination services
type ConnectionInfo struct {
	Host       string // The formatted host string
	SourceInfo string // Source pod in format "ns/pod-name"
	DestInfo   string // Destination service in format "ns/service"
}

func getHost(r *http.Request, reverseDNSRetry int, reverseDNSRetryInternal time.Duration) (ConnectionInfo, error) {
	var connInfo ConnectionInfo
	logger := util.LoggerFromContext(r.Context())
	remoteIP := r.RemoteAddr
	if remoteIP == "" {
		return connInfo, fmt.Errorf("remote address not found")
	}
	// removing port if exists
	if i := strings.Index(remoteIP, ":"); i != -1 {
		remoteIP = remoteIP[:i]
	}

	host := r.Host
	if host == "" {
		return connInfo, fmt.Errorf("host not found")
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
		return connInfo, fmt.Errorf("error looking up address %q: %s", remoteIP, err)
	}

	if len(names) == 0 {
		logger.Error(nil, "No names found for address", "host", host, "remote IP", remoteIP)
		return connInfo, fmt.Errorf("no names found for address %q", remoteIP)
	}
	if len(names) > 1 {
		logger.V(3).Info("More than one name found for address", "remote IP", remoteIP, "DNS names", names, "host", host)
	}
	remoteDNS := selectBestDNSName(names)
	_, remotePod, remoteNs := extractPodInfo(remoteDNS)
	logger.V(1).Info("Routing Info", "host", host, "remote IP", remoteIP, "remote DNS", remoteDNS, "remote Pod", remotePod, "remote Namespace", remoteNs)
	if remoteNs == "" {
		return connInfo, fmt.Errorf("namespace not found in %q", remoteDNS)
	}

	// Source service in format "ns/pod-name"
	connInfo.SourceInfo = fmt.Sprintf("%s/%s", remoteNs, remotePod)

	// Extracting service name and namespace from the host header
	destService, destNs := extractServiceInfo(host)
	logger.V(1).Info("Dest Service and Namespace", "dest Service", destService, "dest Namespace", destNs)

	// If the caller is from user namespace or
	// the destination namespace is not provided in the host header
	// then the destination namespace is the same as the caller namespace
	if strings.HasPrefix(remoteNs, "dp-") || destNs == "" {
		destNs = remoteNs
		connInfo.Host = fmt.Sprintf("%s.%s%s", destService, remoteNs, hostPort)
		logger.V(1).Info("Constructed Host for Source Namespace", "Host", connInfo.Host)
	} else {
		connInfo.Host = fmt.Sprintf("%s.%s%s", destService, destNs, hostPort)
		logger.V(1).Info("Constructed Host for Destination Namespace", "Host", connInfo.Host)
	}

	// Destination service in format "ns/service"
	connInfo.DestInfo = fmt.Sprintf("%s/%s", destNs, destService)

	return connInfo, nil
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

// selectBestDNSName selects the best DNS name from a list of names,
// prioritizing namespaces ending with "choreo-apim"
func selectBestDNSName(names []string) string {
	if len(names) == 0 {
		return ""
	}

	// First, try to find a name with namespace ending in "choreo-apim"
	for _, name := range names {
		_, _, namespace := extractPodInfo(name)
		if strings.HasSuffix(namespace, "choreo-apim") {
			return name
		}
	}

	// If no "choreo-apim" namespace found, return the first name
	return names[0]
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
