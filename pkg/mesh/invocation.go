package mesh

import (
	"net/url"
	"strings"
)

func (n *Node) beginInvocation(principal, path string) *RemoteError {
	n.mu.Lock()
	defer n.mu.Unlock()
	// ponytail: fixed 64/node and 32/principal budgets; expose configuration only
	// when measured workloads require different admission limits.
	if (n.closed || n.manifest.Lifecycle.Draining) && path != "/.well-known/whalemesh" && !strings.HasPrefix(path, "/mesh/") {
		return &RemoteError{Status: 503, Code: "draining", Message: "node is draining"}
	}
	if n.inflightTotal >= 64 || n.inflight[principal] >= 32 {
		return &RemoteError{Status: 429, Code: "backpressure", Message: "authenticated request budget exhausted"}
	}
	if n.inflight == nil {
		n.inflight = make(map[string]int)
	}
	n.inflightTotal++
	n.inflight[principal]++
	return nil
}
func (n *Node) endInvocation(principal string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.inflightTotal--
	n.inflight[principal]--
	if n.inflight[principal] == 0 {
		delete(n.inflight, principal)
	}
}

// Encryption is an interface trait, never inferred from a business capability
// or permission name. The provider enforces its own advertised route policy.
func (n *Node) operationRequiresTLS(method, path string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, capability := range n.manifest.Provides {
		for _, operation := range capability.Operations {
			if (capability.TransportSecurity == "tls" || operation.TransportSecurity == "tls") && operationMatches(operation, method, path) {
				return true
			}
		}
		if len(capability.Operations) == 0 && capability.TransportSecurity == "tls" {
			endpoint, err := url.Parse(capability.Endpoint)
			if err == nil && operationMatches(Operation{Method: method, Path: endpoint.Path}, method, path) {
				return true
			}
		}
	}
	return false
}
