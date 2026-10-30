package knowledgeoperator

import (
	"errors"
	"runtime/debug"
	"strings"
)

// BuildRevision is pinned by the release build's -ldflags from its immutable source archive.
var BuildRevision string

func SourceRevision() (string, error) {
	var embedded string
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				embedded = setting.Value
			}
		}
	}
	if embedded != "" && !revisionRE.MatchString(embedded) {
		return "", errors.New("invalid embedded source revision")
	}
	if BuildRevision != "" && !revisionRE.MatchString(BuildRevision) {
		return "", errors.New("invalid pinned source revision")
	}
	if embedded != "" && BuildRevision != "" && strings.TrimPrefix(embedded, "vcs.revision=") != BuildRevision {
		return "", errors.New("source revision conflict")
	}
	revision := BuildRevision
	if revision == "" {
		revision = embedded
	}
	if !revisionRE.MatchString(revision) {
		return "", errors.New("source revision unavailable")
	}
	return revision, nil
}
