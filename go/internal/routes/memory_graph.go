package routes

import (
	"net/http"
	"strings"

	"odysseus/internal/db"
)

type memoryGraphRoutes struct {
	db *db.DB
}

// MemoryGraphRoutes registers the memory bubble graph endpoint.
func MemoryGraphRoutes(mux *http.ServeMux, database *db.DB) {
	r := &memoryGraphRoutes{db: database}
	mux.HandleFunc("GET /api/memory/graph", r.graph)
}

// ── Graph types ──────────────────────────────────────────────────

type memNode struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Group     string `json:"group"`
	Content   string `json:"content,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	Size      int    `json:"size"`
}

type memLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type memGraph struct {
	Nodes []memNode `json:"nodes"`
	Links []memLink `json:"links"`
}

func (r *memoryGraphRoutes) graph(w http.ResponseWriter, req *http.Request) {
	owner := effectiveUser(req)

	memories, err := r.db.ListMemoriesForGraph(owner)
	if err != nil {
		jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}

	var nodes []memNode
	var links []memLink

	add := func(n memNode) { nodes = append(nodes, n) }
	link := func(src, tgt string) { links = append(links, memLink{Source: src, Target: tgt}) }

	// Central hub
	add(memNode{ID: "__hub__", Label: "MEMORIES", Group: "hub", Size: 50})

	// Group memories by category
	type catEntry struct {
		mem   db.MemoryRow
		group string
	}
	groupCounts := map[string]int{}
	var entries []catEntry
	for _, m := range memories {
		grp := resolveMemGroup(m.Category)
		groupCounts[grp]++
		entries = append(entries, catEntry{mem: m, group: grp})
	}

	// Create category group nodes and leaf nodes
	addedGroups := map[string]bool{}
	for _, e := range entries {
		if !addedGroups[e.group] {
			addedGroups[e.group] = true
			sz := 26 + groupCounts[e.group]*3
			if sz < 32 {
				sz = 32
			}
			if sz > 60 {
				sz = 60
			}
			add(memNode{
				ID:    "mem_grp:" + e.group,
				Label: strings.ToUpper(e.group),
				Group: "mem_category",
				Size:  sz,
			})
			link("__hub__", "mem_grp:"+e.group)
		}

		// Truncate content for the graph payload
		content := e.mem.Text
		if len(content) > 200 {
			content = content[:197] + "..."
		}

		add(memNode{
			ID:      "mem:" + e.mem.ID,
			Label:   truncLabel(e.mem.Text, 40),
			Group:   "mem_leaf",
			Content: content,
			Size:    7,
		})
		link("mem_grp:"+e.group, "mem:"+e.mem.ID)
	}

	if nodes == nil {
		nodes = []memNode{}
	}
	if links == nil {
		links = []memLink{}
	}

	writeJSON(w, http.StatusOK, memGraph{Nodes: nodes, Links: links})
}

// resolveMemGroup maps a memory category to a graph group name.
func resolveMemGroup(category string) string {
	cat := strings.ToLower(strings.TrimSpace(category))
	if cat == "" || cat == "fact" {
		return "fact"
	}
	return cat
}

// truncLabel returns a short label suitable for bubble display.
func truncLabel(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	// Use first line only
	if idx := strings.IndexAny(text, "\n\r"); idx >= 0 {
		text = text[:idx]
	}
	if len(text) > maxLen {
		return text[:maxLen-1] + "\u2026"
	}
	return text
}
