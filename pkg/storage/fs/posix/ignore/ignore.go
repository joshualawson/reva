package ignore

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/blobstore"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/lookup"
	"github.com/opencloud-eu/reva/v3/pkg/storage/fs/posix/options"
	"github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/tree/propagator"
	"github.com/opencloud-eu/reva/v3/pkg/storage/utils/templates"
	"github.com/rs/zerolog"
)

const (
	// The needles include the separator at the beginning to avoid matching internal dir names in substrings
	// The end segment boundary is checked in isInternalDir
	trashNeedle    = needle(string(filepath.Separator) + lookup.TrashDir)
	metadataNeedle = needle(string(filepath.Separator) + lookup.MetadataDir)
	tmpNeedle      = needle(string(filepath.Separator) + blobstore.TMPDir)
)

type needle string

// Ignorer handles checking if paths should be ignored in posix operations
type Ignorer struct {
	options *options.Options
	log     *zerolog.Logger
	root    string
	// the directories whose children are space roots: personal, project and, when configured, fallback personal
	spacesRoots []string
}

// NewIgnorer creates a new Ignorer given the posix options and logger
func NewIgnorer(options *options.Options, log *zerolog.Logger) *Ignorer {
	i := &Ignorer{
		options: options,
		log:     log,
		root:    filepath.Clean(options.Root),
		spacesRoots: []string{
			filepath.Clean(filepath.Join(options.Root, templates.Base(options.PersonalSpacePathTemplate))),
			filepath.Clean(filepath.Join(options.Root, templates.Base(options.GeneralSpacePathTemplate))),
		},
	}
	if options.PersonalSpaceFallbackPathTemplate != "" {
		i.spacesRoots = append(i.spacesRoots, filepath.Clean(filepath.Join(options.Root, templates.Base(options.PersonalSpaceFallbackPathTemplate))))
	}
	return i
}

// IsIgnored checking if paths should be ignored in posix operations
func (i *Ignorer) IsIgnored(path string) bool {
	return i.IsIgnoredDir(path) ||
		i.IsChanges(path) ||
		i.IsIndex(path) ||
		IsLockFile(path) ||
		i.IsTrash(path) ||
		i.IsMetadata(path) ||
		i.IsTemporary(path) ||
		i.IsUpload(path) ||
		i.IsRootPath(path) ||
		i.IsSpaceRoot(path) ||
		i.IsPurging(path)
}

func (i *Ignorer) IsChanges(path string) bool {
	return strings.HasPrefix(path, filepath.Join(i.options.Root, propagator.ChangesDir))
}

func (i *Ignorer) IsIndex(path string) bool {
	return strings.HasPrefix(path, filepath.Join(i.options.Root, lookup.IndexesDir))
}

func (i *Ignorer) IsUpload(path string) bool {
	return strings.HasPrefix(path, i.options.UploadDirectory)
}

func (i *Ignorer) IsRootPath(path string) bool {
	return path == i.root || slices.Contains(i.spacesRoots, path)
}

func (i *Ignorer) IsSpaceRoot(path string) bool {
	return slices.Contains(i.spacesRoots, filepath.Dir(path))
}

// IsIgnoredDir reports whether path is, or lies below, a directory under the root whose name matches ignored_dir_patterns
func (i *Ignorer) IsIgnoredDir(path string) bool {
	if len(i.options.IgnoredDirPatterns) == 0 {
		return false
	}
	rel, ok := strings.CutPrefix(filepath.Clean(path), i.root+string(filepath.Separator))
	if !ok {
		return false
	}
	segments := strings.Split(rel, string(filepath.Separator))
	for n, segment := range segments {
		if !i.MatchesIgnoredDirName(segment) {
			continue
		}
		// a dot-file is still a file: the last segment only counts when it is a directory on disk
		if n < len(segments)-1 {
			return true
		}
		if fi, err := os.Lstat(path); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// MatchesIgnoredDirName reports whether a directory called name is one of the ignored ones
func (i *Ignorer) MatchesIgnoredDirName(name string) bool {
	for _, pattern := range i.options.IgnoredDirPatterns {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
	}
	return false
}

// IsPurging checks if the path lies in a space root that was renamed for purging
func (i *Ignorer) IsPurging(path string) bool {
	for _, root := range i.spacesRoots {
		if rel, ok := strings.CutPrefix(path, root+string(filepath.Separator)); ok && strings.HasPrefix(rel, lookup.PurgeDirPrefix) {
			return true
		}
	}
	return false
}

func IsLockFile(path string) bool {
	return strings.HasSuffix(path, ".flock") || strings.HasSuffix(path, ".mlock")
}

func (i *Ignorer) IsMetadata(path string) bool {
	return i.isInternalDir(path, metadataNeedle)
}

func (i *Ignorer) IsTemporary(path string) bool {
	return i.isInternalDir(path, tmpNeedle)
}

func (i *Ignorer) IsTrash(path string) bool {
	return i.isInternalDir(path, trashNeedle)
}

// isInternalDir checks if the path contains the match dir and that the match lives
// in the space root, e.g. "/storage/users/user1/.metadata/file" -> match is ".metadata",
// parent dir is "/storage/users/user1" which is a space root, so this would return true
func (i *Ignorer) isInternalDir(path string, match needle) bool {
	idx := strings.Index(path, string(match))
	if idx <= 0 {
		return false
	}

	// must end at a segment boundary (end of path or separator)
	if length := idx + len(match); length != len(path) && path[length] != filepath.Separator {
		return false
	}

	// get the path of the parent dir, e.g. "/a/match" ->  index of "match" is 3
	// so parentPath is path[:2] -> "/a"
	parentPath := path[:idx-1]

	return len(parentPath) > 0 && i.IsSpaceRoot(parentPath)
}
