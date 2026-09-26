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
	"sort"
	"strings"
	"sync"
	"time"
)

const activeFileName = "access.jsonl"

type fileSink struct {
	mu       sync.Mutex
	dir      string
	config   FileConfig
	file     *os.File
	size     int64
	period   string
	sequence uint64
}

func newFileSink(dir string, config FileConfig) (*fileSink, error) {
	if dir == "" {
		dir = "logs"
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0750); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, activeFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0640); err != nil {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	period := ""
	if info.Size() > 0 {
		period = rotationPeriod(config.Rotation, info.ModTime())
	}
	return &fileSink{dir: dir, config: config, file: file, size: info.Size(), period: period}, nil
}

func (s *fileSink) Write(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalizeRecordTime(&record)
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return fmt.Errorf("access log file is not open")
	}
	period := rotationPeriod(s.config.Rotation, record.Timestamp)
	rotate := s.size > 0 && period != "" && s.period != "" && period != s.period
	if s.config.Rotation == "size" && s.size > 0 && s.size+int64(len(line)) > s.config.MaxSizeBytes {
		rotate = true
	}
	if rotate {
		if err := s.rotate(record.Timestamp); err != nil {
			return err
		}
	}
	if _, err := s.file.Write(line); err != nil {
		return err
	}
	s.size += int64(len(line))
	if period != "" {
		s.period = period
	}
	return nil
}

func rotationPeriod(mode string, at time.Time) string {
	at = at.UTC()
	switch mode {
	case "day":
		return at.Format("20060102")
	case "hour":
		return at.Format("2006010215")
	default:
		return ""
	}
}

func (s *fileSink) rotate(at time.Time) error {
	if err := s.file.Close(); err != nil {
		return err
	}
	s.sequence++
	rawPath := filepath.Join(s.dir, fmt.Sprintf("access-%s-%06d.jsonl", at.UTC().Format("20060102T150405.000000000Z"), s.sequence))
	activePath := filepath.Join(s.dir, activeFileName)
	if err := os.Rename(activePath, rawPath); err != nil {
		file, openErr := os.OpenFile(activePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
		if openErr == nil {
			s.file = file
		}
		return err
	}
	file, err := os.OpenFile(activePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	if err := file.Chmod(0640); err != nil {
		_ = file.Close()
		return err
	}
	s.file = file
	s.size = 0
	s.period = rotationPeriod(s.config.Rotation, at)
	if s.config.Compress {
		if err := gzipFile(rawPath); err != nil {
			return err
		}
	}
	return s.pruneArchives()
}

func gzipFile(path string) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	outputPath := path + ".gz"
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	writer := gzip.NewWriter(output)
	_, copyErr := io.Copy(writer, input)
	closeErr := writer.Close()
	fileErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(outputPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(outputPath)
		return closeErr
	}
	if fileErr != nil {
		_ = os.Remove(outputPath)
		return fileErr
	}
	return os.Remove(path)
}

func (s *fileSink) pruneArchives() error {
	if s.config.KeepFiles == 0 {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(s.dir, "access-*.jsonl*"))
	if err != nil {
		return err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	if len(files) <= s.config.KeepFiles {
		return nil
	}
	for _, path := range files[s.config.KeepFiles:] {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
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
	var records []Record
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return SearchResult{}, err
		}
		entries, err := readRecords(ctx, path, query)
		if err != nil {
			return SearchResult{}, err
		}
		records = append(records, entries...)
	}
	return paginate(records, query), nil
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

func (s *fileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}
