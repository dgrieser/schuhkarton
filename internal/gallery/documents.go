package gallery

import (
	"path"
	"strings"
)

// DefaultDocFolders are the folder names whose files count as documents
// when none are configured.
var DefaultDocFolders = []string{"Dokumente", "Documents", "Scans", "Belege"}

// IsDocument reports whether an item lies below a documents folder (the
// root's own name counts too). Galleries hide documents unless asked for.
func (l *Library) IsDocument(it *Item) bool {
	return l.isDocumentAt(it.Root, it.Rel)
}

// isDocumentAt reports whether a file at rel in root ri counts as a document.
func (l *Library) isDocumentAt(ri int, rel string) bool {
	names := l.opt.DocFolders
	if names == nil {
		names = DefaultDocFolders
	}
	if len(names) == 0 {
		return false
	}
	parts := []string{l.opt.Roots[ri].Name}
	if dir := path.Dir(rel); dir != "." {
		parts = append(parts, strings.Split(dir, "/")...)
	}
	for _, p := range parts {
		fp := fold(p)
		for _, n := range names {
			if fp == fold(n) {
				return true
			}
		}
	}
	return false
}
