package runner

import (
	"strconv"
	"strings"
)

// diffFile is what one file's section of a unified diff says about its path.
type diffFile struct {
	// named is the one path the `diff --git` header names for both sides.
	named string
	// moved are the sources and destinations of a rename or copy.
	moved []string
	// marked are the paths the `---` and `+++` lines name, kept for a header
	// that does not read.
	marked []string
	inHunk bool
}

// paths are the repository paths the section touches, the most exact source
// first: a rename's two sides, then the header's path, then the marker lines.
func (f diffFile) paths() []string {
	switch {
	case len(f.moved) > 0:
		return f.moved
	case f.named != "":
		return []string{f.named}
	}
	return f.marked
}

var diffMovedPrefixes = []string{"rename from ", "rename to ", "copy from ", "copy to "}

// diffPaths returns every repository path a unified diff touches, both sides of
// a rename or copy included. A path git quoted, for a quote, backslash, control
// character or non-ASCII byte, is returned as the real path.
func diffPaths(diff string) []string {
	var paths []string
	var cur *diffFile
	flush := func() {
		if cur != nil {
			paths = append(paths, cur.paths()...)
		}
	}
	for l := range strings.SplitSeq(diff, "\n") {
		if rest, ok := strings.CutPrefix(l, "diff --git "); ok {
			flush()
			cur = &diffFile{}
			cur.named, _ = headerPath(rest)
			continue
		}
		// Hunk lines start with a space, + or -, so a metadata line is only
		// read ahead of a section's first hunk.
		if cur == nil || cur.inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(l, "@@"):
			cur.inHunk = true
		case strings.HasPrefix(l, "--- "):
			cur.addMarked(strings.TrimPrefix(l, "--- "), "a/")
		case strings.HasPrefix(l, "+++ "):
			cur.addMarked(strings.TrimPrefix(l, "+++ "), "b/")
		default:
			for _, prefix := range diffMovedPrefixes {
				if p, ok := strings.CutPrefix(l, prefix); ok {
					if path, ok := unquotePath(p); ok {
						cur.moved = append(cur.moved, path)
					}
				}
			}
		}
	}
	flush()
	return paths
}

// addMarked records the path a `---` or `+++` line names.
func (f *diffFile) addMarked(p, prefix string) {
	if strings.HasPrefix(p, `"`) {
		// git adds a tab after a quoted name too when the line carries a timestamp.
		p = strings.TrimRight(p, "\t")
	} else {
		// A name holding a space is followed by a tab and a timestamp or nothing.
		p, _, _ = strings.Cut(p, "\t")
	}
	// An unreadable name is empty and /dev/null has neither prefix, so none names a path.
	path, _ := unquotePath(p)
	if path, ok := strings.CutPrefix(path, prefix); ok {
		f.marked = append(f.marked, path)
	}
}

// headerPath reads the path from the text after `diff --git `, which names the
// same path on both sides unless the file moved. A name with a space makes an
// unquoted header ambiguous, so the split taken is the one whose two sides name
// the same path.
func headerPath(rest string) (string, bool) {
	if strings.HasPrefix(rest, `"`) {
		end := quotedEnd(rest)
		if end < 0 || end+1 >= len(rest) || rest[end+1] != ' ' {
			return "", false
		}
		return samePath(rest[:end+1], rest[end+2:])
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] != ' ' {
			continue
		}
		if p, ok := samePath(rest[:i], rest[i+1:]); ok {
			return p, true
		}
	}
	return "", false
}

// samePath reports the path two header halves name when it is the same: a/<p>
// and b/<p>, each possibly quoted.
func samePath(a, b string) (string, bool) {
	aPath, aOK := unquotePath(a)
	bPath, bOK := unquotePath(b)
	if !aOK || !bOK {
		return "", false
	}
	aPath, aOK = strings.CutPrefix(aPath, "a/")
	bPath, bOK = strings.CutPrefix(bPath, "b/")
	if !aOK || !bOK || aPath != bPath {
		return "", false
	}
	return aPath, true
}

// quotedEnd is the index of the quote closing the quoted name s starts with, or
// -1 when it never closes.
func quotedEnd(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// unquotePath reads a name git wrote: a quoted one is C-style escaped, with
// non-ASCII bytes as octal escapes, and a bare one is itself. A quoted name
// must be the whole of s.
func unquotePath(s string) (string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return s, s != ""
	}
	p, err := strconv.Unquote(s)
	return p, err == nil
}
