// Package telemlineage builds the lineage metadata that lets Telem group an
// agent tree's searches: identifiers, plus the agent's task as its goal.
package telemlineage

import (
	"time"

	"github.com/google/uuid"
)

// Call identifies one tool call inside one agent.
type Call struct {
	AgentID            uuid.UUID
	WindowID           uuid.UUID // latest context checkpoint at the model call; uuid.Nil means none
	ModelCallContextID uuid.UUID
	ToolCallID         uuid.UUID
	CreatedAt          time.Time
}

type Lineage struct {
	Call Call
	Goal string // the agent's task; labels its searches
	// Ancestors lists, root first, the spawn_agent call in each parent agent
	// that created the next agent down the tree.
	Ancestors []Call
}

func (l Lineage) IsZero() bool {
	return l.Call == (Call{}) && len(l.Ancestors) == 0
}

// Metadata is the lineage block for one search or fetch. kind is "search" or
// "fetch" and must match the endpoint. A zero Lineage returns nil, and the
// caller then sends no lineage.
func Metadata(l Lineage, kind string) map[string]any {
	if l.IsZero() {
		return nil
	}
	ancestors := snapshots(l.Ancestors)
	session := sessionOf(l.Call)
	metadata := map[string]any{
		"session_key": session,
		"fingerprint": fingerprint(harness, l.Call.AgentID.String()),
		"node_key": eventNodeKey(
			harness, session, l.Call.ModelCallContextID.String(), l.Call.ToolCallID.String(),
		),
		"parent_node_key": parentNodeKey(ancestors),
		"ancestors":       ancestors,
		"kind":            kind,
	}
	if l.Goal != "" {
		metadata["goal"] = l.Goal
	}
	return metadata
}

// snapshots identifies each parent agent at the model call that spawned the
// next agent down, root first, each entry linked to the one above it.
func snapshots(spawns []Call) []map[string]any {
	chain := make([]map[string]any, 0, len(spawns))
	for _, spawn := range spawns {
		conversation := spawn.AgentID.String()
		var spawnedAt any
		if !spawn.CreatedAt.IsZero() {
			spawnedAt = spawn.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		chain = append(chain, map[string]any{
			"session_key":     sessionKey(harness, conversation, windowOf(spawn)),
			"fingerprint":     fingerprint(harness, conversation),
			"node_key":        snapshotNodeKey(harness, conversation, spawn.ModelCallContextID.String()),
			"parent_node_key": parentNodeKey(chain),
			"spawned_at":      spawnedAt,
		})
	}
	return chain
}

func sessionOf(c Call) string {
	return sessionKey(harness, c.AgentID.String(), windowOf(c))
}

// parentNodeKey links a node to the last entry of a root-first chain.
func parentNodeKey(chain []map[string]any) any {
	if len(chain) == 0 {
		return nil
	}
	return chain[len(chain)-1]["node_key"]
}

func windowOf(c Call) string {
	if c.WindowID == uuid.Nil {
		return none
	}
	return c.WindowID.String()
}
