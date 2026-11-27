package indexer

import (
	"time"

	"codesseek/internal/scanner"
	"codesseek/internal/store"
)

// RefreshAge is how old an index may get before it is treated as stale even
// when every recorded file still looks unchanged.
const RefreshAge = 24 * time.Hour

// NeedsRefresh reports whether the on-disk index for root no longer matches
// the working tree. It compares the recorded file set, sizes and mtimes with a
// fresh scan, so it catches edits, additions and deletions without hashing.
//
// The check is intentionally cheap (stat only): it decides whether to run an
// incremental index, not whether the content is semantically identical.
func NeedsRefresh(root, dataDir string, scanOpt scanner.Options) (bool, error) {
	st, err := store.New(dataDir, root)
	if err != nil {
		return false, err
	}
	meta, err := st.LoadMeta()
	if err != nil {
		return false, err
	}
	if time.Since(meta.IndexedAt) > RefreshAge {
		return true, nil
	}

	recorded, err := st.LoadFiles()
	if err != nil {
		return false, err
	}
	old := make(map[string]store.FileRecord, len(recorded))
	for _, f := range recorded {
		old[f.Path] = f
	}

	current, err := scanner.Scan(root, scanOpt)
	if err != nil {
		return false, err
	}
	if len(current) != len(recorded) {
		return true, nil
	}
	for _, f := range current {
		prev, ok := old[f.Path]
		if !ok || prev.Size != f.Size || prev.MTime != f.MTime {
			return true, nil
		}
	}
	return false, nil
}
