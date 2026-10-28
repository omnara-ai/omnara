package agentconfig

// CurrentSourceRevision is the config revision new versions are saved under.
// A migration that rewrites compiled definitions so saved sources no longer
// match them bumps it and records why in sourceRevisionChanges.
const CurrentSourceRevision = 1

// SourceRevisionChange explains, for sources saved before Revision, what
// changed when Omnara moved to it.
type SourceRevisionChange struct {
	Revision int
	Notice   string
	// Affects reports whether the change applies to a saved source. Nil means
	// it applies to every source.
	Affects func(source string) bool
}

// sourceRevisionChanges lists every revision after 1, in order.
var sourceRevisionChanges = []SourceRevisionChange{}

// SourceNotices explains why a source saved under revision may no longer
// match its compiled definition. It is empty for current sources.
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
