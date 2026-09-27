package accesslog

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// searchGlobHook, when set (tests only), runs synchronously right after Search lists its candidate files and
// before it opens or filters any of them. Tests use it to deterministically inject the effect of a concurrent
// archive/prune step (both of which touch files outside s.mu; see archiveSlot and pruneArchives) exactly inside
// the race window readRecordsTolerant exists to handle, instead of relying on goroutine scheduling to land inside
// a window a few instructions wide.
var searchGlobHook func(paths []string)

// skipShadowedArchiving drops any ".archiving" path from paths when this same Glob snapshot also contains that
// period's finished ".gz" form. The two can briefly coexist on disk: gzipFileTo renames the ".gz" into place
// before removing the ".archiving" source it compressed from (see its doc comment), so a Glob landing in that
// window returns both names for what is really one period's records. The ".gz" is always the complete copy in
// that window (readRecordsTolerant's ENOENT fallback handles the ".archiving"-vanishes-later case separately),
// so it wins and the ".archiving" copy is left out to avoid double-counting every record in it.
func skipShadowedArchiving(paths []string) []string {
	gz := make(map[string]bool, len(paths))
	for _, p := range paths {
		if strings.HasSuffix(p, ".gz") {
			gz[p] = true
		}
	}
	if len(gz) == 0 {
		return paths
	}
	kept := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.HasSuffix(p, ".archiving") && gz[strings.TrimSuffix(p, ".archiving")+".gz"] {
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

func (s *fileSink) Search(ctx context.Context, query Query) (SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths, err := filepath.Glob(filepath.Join(s.dir, "access-*.jsonl*"))
	if err != nil {
		return SearchResult{}, err
	}
	active := filepath.Join(s.dir, activeFileName)
	if _, err := os.Stat(active); err == nil {
		paths = append(paths, active)
	}
	if searchGlobHook != nil {
		searchGlobHook(paths)
	}
	paths = skipShadowedArchiving(paths)
	var records []Record
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return SearchResult{}, err
		}
		entries, err := readRecordsTolerant(ctx, path, query)
		if err != nil {
			return SearchResult{}, err
		}
		records = append(records, entries...)
	}
	return paginate(records, query), nil
}

// readRecordsTolerant reads path for Search, tolerating the file having vanished after Search's directory
// listing: archiveSlot and pruneArchives both touch files outside s.mu (so a slow gzip or prune never blocks the
// write path — see rotateActiveLocked), so between Search's Glob and this call's Open, a file it listed can have
// been renamed or removed by either one. A missing ".archiving" file (the fixed suffix archiveSlot renames a
// period's primary/shard to while it compresses it) is followed to wherever the compression left it: its
// finished ".gz" form in the common case, or back under its original plain name if the compression failed and
// archiveSlot rolled it back (see archiveSlot's recovery branch). A missing file of any other shape was simply
// removed by retention (see pruneArchives) and is left out of the result rather than failing the whole query.
// Any other error — permission, a truncated/corrupt file, a JSON parse failure — is a real problem and is still
// returned, matching Search's existing behavior for genuine I/O errors.
func readRecordsTolerant(ctx context.Context, path string, query Query) ([]Record, error) {
	records, err := readRecords(ctx, path, query)
	if err == nil || !os.IsNotExist(err) {
		return records, err
	}
	if !strings.HasSuffix(path, ".archiving") {
		return nil, nil
	}
	base := strings.TrimSuffix(path, ".archiving")
	for _, fallback := range []string{base + ".gz", base} {
		records, err := readRecords(ctx, fallback, query)
		if err == nil {
			return records, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return nil, nil
}

func readRecords(ctx context.Context, path string, query Query) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var reader io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			return nil, err
		}
		defer gzipReader.Close()
		reader = gzipReader
	}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var found []Record
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, readErr := buffered.ReadBytes('\n')
		if len(line) > 0 {
			var record Record
			if err := json.Unmarshal(line, &record); err != nil {
				return nil, fmt.Errorf("parse access log %s: %w", filepath.Base(path), err)
			}
			if matches(record, query) {
				found = append(found, record)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return found, nil
}
