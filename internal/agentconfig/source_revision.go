package agentconfig

// CurrentSourceRevision is bumped by migrations that rewrite compiled
// definitions so saved sources no longer match, with an entry in
// sourceRevisionChanges explaining why.
const CurrentSourceRevision = 1

type SourceRevisionChange struct {
	Revision int
	Notice   string
	// Affects limits the notice to matching sources; nil applies it to all.
	Affects func(source string) bool
}

var sourceRevisionChanges = []SourceRevisionChange{}

func SourceNotices(revision int, source string) []string {
	return sourceNotices(sourceRevisionChanges, revision, source)
}

func sourceNotices(changes []SourceRevisionChange, revision int, source string) []string {
	if source == "" {
		return nil
	}
	var notices []string
	for _, change := range changes {
		if change.Revision <= revision || (change.Affects != nil && !change.Affects(source)) {
			continue
		}
		notices = append(notices, change.Notice)
	}
	return notices
}
