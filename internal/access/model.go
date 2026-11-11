// Package access decides what the caller may see (F-027, D-147). Ownership lives only on
// top-level folders and lists; anything nested inherits access from its top-level ancestor, the
// "box". A resource is visible when its box is visible, and Snapshot.Visible is the one place
// that rule is written: the box is the caller's, or its top-level folder is shared with the caller.
//
// The package has no HTTP and no business operations: other features reach it through small
// interfaces they declare, wired in httpapi/router.go. Background jobs (sweeper, auto-purge,
// lookup refresh) work on the whole workspace and never go through it.
package access

import "github.com/google/uuid"

// Kind names the resource a path parameter or a body reference points to.
type Kind string

const (
	KindFolder     Kind = "folder"
	KindList       Kind = "list"
	KindItem       Kind = "item"
	KindAttachment Kind = "attachment"
	KindTrashBatch Kind = "trash_batch"
)

// Node is a folder or a list of the snapshot. ParentID is the parent folder (folder) or the
// containing folder (list); nil is the top level. Owner is set only on top-level rows.
type Node struct {
	ID       uuid.UUID
	ParentID *uuid.UUID
	Owner    *uuid.UUID
	Trashed  bool
}

// Box is the top-level ancestor of a resource. Found is false when the resource (or part of its
// ancestry) does not exist. Owner is uuid.Nil for a top-level row nobody owns.
type Box struct {
	Found bool
	Owner uuid.UUID
	// TopFolder is the top-level folder of the box (uuid.Nil when the box is a top-level list);
	// its members see the box.
	TopFolder uuid.UUID
}

// Snapshot is every folder and list of the workspace in any state, read once per request. It is
// immutable, so it can be shared by the guard, the body checks and the filtered collections.
type Snapshot struct {
	folders map[uuid.UUID]Node
	lists   map[uuid.UUID]Node
	members map[Member]bool
}

// Member is one row of folder_members: User may see the top-level folder Folder.
type Member struct{ Folder, User uuid.UUID }

// NewSnapshot indexes the folders, lists and memberships. A membership of a folder that is not
// top level, or does not exist, is kept but never read: the rule only asks for the members of a
// box's top-level folder, so stale rows are inert.
func NewSnapshot(folders, lists []Node, members ...Member) Snapshot {
	s := Snapshot{folders: make(map[uuid.UUID]Node, len(folders)), lists: make(map[uuid.UUID]Node, len(lists)), members: make(map[Member]bool, len(members))}
	for _, m := range members {
		s.members[m] = true
	}
	for _, f := range folders {
		s.folders[f.ID] = f
	}
	for _, l := range lists {
		s.lists[l.ID] = l
	}
	return s
}

// FolderBox returns the box of a folder in any state. The step cap keeps a corrupt cycle finite.
func (s Snapshot) FolderBox(id uuid.UUID) Box {
	cur := id
	for range len(s.folders) + 1 {
		f, ok := s.folders[cur]
		if !ok {
			return Box{}
		}
		if f.ParentID == nil {
			return Box{Found: true, Owner: ownerOf(f), TopFolder: f.ID}
		}
		cur = *f.ParentID
	}
	return Box{}
}

// ListBox returns the box of a list in any state.
func (s Snapshot) ListBox(id uuid.UUID) Box {
	l, ok := s.lists[id]
	if !ok {
		return Box{}
	}
	if l.ParentID == nil {
		return Box{Found: true, Owner: ownerOf(l)}
	}
	return s.FolderBox(*l.ParentID)
}

// Visible is the single visibility rule: user may see the resources of box b when the box is
// theirs (they own it), or its top-level folder is shared with them (they are a member).
func (s Snapshot) Visible(user uuid.UUID, b Box) bool {
	if !b.Found || user == uuid.Nil {
		return false
	}
	return b.Owner == user || (b.TopFolder != uuid.Nil && s.members[Member{b.TopFolder, user}])
}

// VisibleFolders returns the active folders whose box user may see.
func (s Snapshot) VisibleFolders(user uuid.UUID) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for id, f := range s.folders {
		if !f.Trashed && s.Visible(user, s.FolderBox(id)) {
			out[id] = true
		}
	}
	return out
}

// VisibleLists returns the active lists whose box user may see.
func (s Snapshot) VisibleLists(user uuid.UUID) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for id, l := range s.lists {
		if !l.Trashed && s.Visible(user, s.ListBox(id)) {
			out[id] = true
		}
	}
	return out
}

func ownerOf(n Node) uuid.UUID {
	if n.Owner == nil {
		return uuid.Nil
	}
	return *n.Owner
}

// Root types of a trash batch (the trash_batches.root_type column).
const (
	rootFolder = "folder"
	rootList   = "list"
	rootItem   = "item"
)

// TrashRoot is the root of a trash batch. ItemList is the list of an item root.
type TrashRoot struct {
	BatchID  uuid.UUID
	Type     string
	RootID   uuid.UUID
	ItemList *uuid.UUID
}
