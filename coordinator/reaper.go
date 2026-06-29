package coordinator

import (
	"context"
	"log"
	"time"
)

// StartReaperLoop periodically checks for dead nodes and reclaims their channels.
// Runs every 120 seconds. Uses a 180-second heartbeat timeout.
func (c *Coordinator) StartReaperLoop(ctx context.Context) {
	if !c.IsPooled() || c.Client == nil {
		return
	}

	const heartbeatTimeout = 180 * time.Second
	const reaperInterval = 120 * time.Second

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()

		ticker := time.NewTicker(reaperInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-c.stopCh:
				return
			case <-ticker.C:
				c.runReapCycle(heartbeatTimeout)
			}
		}
	}()
}

// runReapCycle finds dead nodes and reclaims their channel assignments.
func (c *Coordinator) runReapCycle(timeout time.Duration) {
	// Repair orphaned rows so downstream counts are accurate.
	if repaired, err := c.Client.RepairOrphanedAssignments(); err != nil {
		log.Printf("[coordinator] reaper: repair orphaned error: %v", err)
	} else if repaired > 0 {
		log.Printf("[coordinator] reaper: repaired %d orphaned assignment(s)", repaired)
	}

	// Find nodes with expired heartbeats (any status).
	deadNodeIDs, err := c.Client.GetDeadNodes(timeout)
	if err != nil {
		log.Printf("[coordinator] reaper: get dead nodes error: %v", err)
		return
	}

	if len(deadNodeIDs) == 0 {
		return
	}

	for _, deadNodeID := range deadNodeIDs {
		if deadNodeID == c.NodeID {
			continue
		}

		reclaimed, err := c.Client.ReclaimChannels(deadNodeID)
		if err != nil {
			log.Printf("[coordinator] reaper: reclaim from %s error: %v", deadNodeID, err)
			continue
		}

		if reclaimed > 0 {
			log.Printf("[coordinator] reaper: reclaimed %d channel(s) from dead node %s",
				reclaimed, deadNodeID)
		}

		if err := c.Client.UpdateNodeStatus(deadNodeID, "offline"); err != nil {
			log.Printf("[coordinator] reaper: update status for %s error: %v", deadNodeID, err)
		}
	}
}
