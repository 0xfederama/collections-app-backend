package trash

import (
	"slices"

	"github.com/google/uuid"
)

// The folder walks run in Go over the rows of FolderParents. They replace the
// recursive CTEs of the old queries (ActiveFolderSubtreeIDs, FolderIsInSubtreeOf,
// FolderPath). Each walk visits a folder once, so corrupt data with a cycle still
// terminates. Ids missing from the rows are not walked into, except the root of
// subtree (the purge removes it whatever the rows say).

// folderIndex maps each folder id to its node.
func folderIndex(nodes []FolderNode) map[uuid.UUID]FolderNode {
	index := make(map[uuid.UUID]FolderNode, len(nodes))
	for _, n := range nodes {
		index[n.ID] = n
	}
	return index
}

// folderChildren maps each folder id to its child folders, in any state.
func folderChildren(nodes []FolderNode) map[uuid.UUID][]FolderNode {
	children := make(map[uuid.UUID][]FolderNode)
	for _, n := range nodes {
		if n.ParentID != nil {
			children[*n.ParentID] = append(children[*n.ParentID], n)
		}
	}
	return children
}

// activeSubtree returns root and its active descendant folders, root first. It is
// nil when root is missing or trashed. The descent stops at a trashed folder: the
// folders below it are not active children of the batch (the old query did the same).
func activeSubtree(nodes []FolderNode, root uuid.UUID) []uuid.UUID {
	r, ok := folderIndex(nodes)[root]
	if !ok || r.Trashed {
		return nil
	}
	children := folderChildren(nodes)
	seen := map[uuid.UUID]bool{root: true}
	out := []uuid.UUID{root}
	for i := 0; i < len(out); i++ {
		for _, c := range children[out[i]] {
			if c.Trashed || seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			out = append(out, c.ID)
		}
	}
	return out
}

// subtree returns root and every folder below it, in any state, root first. The
// purge removes all of them, so root is returned even when nodes does not list it.
func subtree(nodes []FolderNode, root uuid.UUID) []uuid.UUID {
	children := folderChildren(nodes)
	seen := map[uuid.UUID]bool{root: true}
	out := []uuid.UUID{root}
	for i := 0; i < len(out); i++ {
		for _, c := range children[out[i]] {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			out = append(out, c.ID)
		}
	}
	return out
}

// inSubtreeOf reports whether candidate is folder or one of its descendants, in any
// state: the walk goes up from candidate. A restore uses it to tell "into its own
// subtree" (a cycle) from "unavailable". A candidate missing from nodes is in no subtree.
func inSubtreeOf(nodes []FolderNode, candidate, folder uuid.UUID) bool {
	index := folderIndex(nodes)
	n, ok := index[candidate]
	seen := map[uuid.UUID]bool{}
	for ok && !seen[n.ID] {
		if n.ID == folder {
			return true
		}
		seen[n.ID] = true
		if n.ParentID == nil {
			return false
		}
		n, ok = index[*n.ParentID]
	}
	return false
}

// activePath returns folder and its ancestors, root first, while they are active.
// It is empty when folder is missing or trashed. A trashed ancestor ends the path
// (the old FolderPath query did the same).
func activePath(nodes []FolderNode, folder uuid.UUID) []PathSegment {
	index := folderIndex(nodes)
	path := []PathSegment{}
	seen := map[uuid.UUID]bool{}
	n, ok := index[folder]
	for ok && !n.Trashed && !seen[n.ID] {
		seen[n.ID] = true
		path = append(path, PathSegment{Type: TypeFolder, ID: n.ID, Name: n.Name})
		if n.ParentID == nil {
			break
		}
		n, ok = index[*n.ParentID]
	}
	slices.Reverse(path)
	return path
}
