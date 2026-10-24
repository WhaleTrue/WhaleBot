package mesh

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"
)

// Membership is an eventually consistent cache, not a transaction log. Checkpoint
// sequence watermarks in batches; never fsync under the request mutex.
// Successful checkpoints bound crash loss to one interval plus I/O latency;
// failed writes retain the dirty generation and report the larger recovery risk.
func (n *Node) checkpointMembershipLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := n.flushMembership(); err != nil {
				slog.Error("membership checkpoint failed", "error", err)
				n.failures.Add(1)
			}
		}
	}
}
func (n *Node) flushMembership() error {
	if n.config.DataDir == "" {
		return nil
	}
	n.membershipPersist.Lock()
	defer n.membershipPersist.Unlock()
	n.mu.RLock()
	version := n.membershipVersion
	if version == n.membershipFlushed {
		n.mu.RUnlock()
		return nil
	}
	snapshot := make(map[string]watermark, len(n.watermarks))
	for identity, mark := range n.watermarks {
		snapshot[identity] = mark
	}
	n.mu.RUnlock()
	if err := writeAtomic(filepath.Join(n.config.DataDir, "membership.json"), snapshot); err != nil {
		return err
	}
	n.mu.Lock()
	n.membershipFlushed = version
	n.mu.Unlock()
	return nil
}
