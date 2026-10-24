package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Events broadcasts local topology changes. Each subscriber owns its values.
// A slow subscriber drops events; use Manifest/Peers to resynchronize. Closing
// the caller context or the node releases the subscription and closes its channel.
func (n *Node) Events(ctx context.Context) (<-chan TopologyEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil, fmt.Errorf("node is closed")
	}
	if len(n.subscribers) >= 32 {
		n.mu.Unlock()
		return nil, fmt.Errorf("topology subscriber budget exhausted")
	}
	n.nextSubscriber++
	id := n.nextSubscriber
	events := make(chan TopologyEvent, 64)
	n.subscribers[id] = events
	n.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-n.done:
		}
		n.mu.Lock()
		delete(n.subscribers, id)
		close(events)
		n.mu.Unlock()
	}()
	return events, nil
}
func (n *Node) streamTopology(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		WriteError(w, 500, "stream_unsupported", "response cannot flush events")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, err := n.Events(ctx)
	if err != nil {
		WriteError(w, 503, "subscriber_budget", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	send := func(event TopologyEvent) error {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "event: topology\ndata: %s\n\n", data); err != nil {
			return err
		}
		return controller.Flush()
	}
	n.mu.RLock()
	identity := n.manifest.Identity
	identity.Labels = cloneMap(identity.Labels)
	identity.Annotations = cloneMap(identity.Annotations)
	sequence := n.sequence
	n.mu.RUnlock()
	if send(TopologyEvent{Type: "resync", Identity: identity, Sequence: sequence, At: time.Now(), Reason: "read /mesh/status for the current local view"}) != nil {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok || send(event) != nil {
				return
			}
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
		}
	}
}
