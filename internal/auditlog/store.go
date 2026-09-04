package auditlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultDirectory = "./logs"
	filePrefix       = "audit-"
	fileSuffix       = ".jsonl"
	maxRecordBytes   = 16 << 20
	reverseReadChunk = 256 << 10
)

// Record is the durable representation of an audit event. OwnerID is kept in
// the file even though it is not returned by the HTTP API; it is the boundary
// used to enforce tenant isolation when records are read back.
type Record struct {
	ID          string         `json:"id"`
	OwnerID     string         `json:"owner_id"`
	ActorUserID string         `json:"actor_user_id,omitempty"`
	SiteID      string         `json:"site_id,omitempty"`
	AccountID   string         `json:"account_id,omitempty"`
	SiteName    string         `json:"site_name,omitempty"`
	AccountName string         `json:"account_name,omitempty"`
	Action      string         `json:"action"`
	Outcome     string         `json:"outcome"`
	Detail      map[string]any `json:"detail,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

type Page struct {
	Items       []Record `json:"items"`
	Page        int      `json:"page"`
	PageSize    int      `json:"page_size"`
	Total       int      `json:"total"`
	TotalPages  int      `json:"total_pages"`
	HasPrevious bool     `json:"has_previous"`
	HasNext     bool     `json:"has_next"`
}

type PurgeResult struct {
	RemovedFiles   int `json:"removed_files"`
	RewrittenFiles int `json:"rewritten_files"`
	RemovedRecords int `json:"removed_records"`
}

type Store struct {
	directory string
	mu        sync.RWMutex
}

func New(directory string) (*Store, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		directory = defaultDirectory
	}
	directory = filepath.Clean(directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create audit log directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("stat audit log directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("audit log path is not a directory: %s", directory)
	}
	return &Store{directory: directory}, nil
}

func (s *Store) Directory() string {
	if s == nil {
		return ""
	}
	return s.directory
}

func (s *Store) Append(record Record) error {
	if s == nil {
		return errors.New("audit log store is nil")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	} else {
		record.CreatedAt = record.CreatedAt.UTC()
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	if len(encoded) > maxRecordBytes {
		return fmt.Errorf("audit event exceeds %d byte limit", maxRecordBytes)
	}
	encoded = append(encoded, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.directory, filePrefix+record.CreatedAt.Format("2006-01-02")+fileSuffix)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log file: %w", err)
	}
	_, writeErr := file.Write(encoded)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write audit log event: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close audit log file: %w", closeErr)
	}
	return nil
}

func (s *Store) List(ownerID string, page, pageSize int) (Page, error) {
	if s == nil {
		return Page{}, errors.New("audit log store is nil")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return Page{}, errors.New("audit log owner ID is required")
	}
	if page < 1 {
		return Page{}, errors.New("audit log page must be positive")
	}
	if pageSize < 1 {
		return Page{}, errors.New("audit log page size must be positive")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	paths, err := s.listFilePaths()
	if err != nil {
		return Page{}, err
	}
	skip := (page - 1) * pageSize
	need := skip + pageSize + 1
	newest := make([]Record, 0, need)
	seen := map[string]struct{}{}
	for _, path := range paths {
		if len(newest) >= need {
			break
		}
		batch, err := s.collectOwnerRecordsFromEnd(path, ownerID, seen, need-len(newest))
		if err != nil {
			return Page{}, err
		}
		newest = append(newest, batch...)
	}
	items := make([]Record, 0, pageSize)
	hasMore := false
	if skip < len(newest) {
		items = newest[skip:]
		if len(items) > pageSize {
			hasMore = true
			items = items[:pageSize]
		}
	}
	total := (page-1)*pageSize + len(items)
	if hasMore {
		total++
	}
	totalPages := page
	if hasMore {
		totalPages = page + 1
	} else if total == 0 {
		totalPages = 0
	}
	return Page{
		Items:       items,
		Page:        page,
		PageSize:    pageSize,
		Total:       total,
		TotalPages:  totalPages,
		HasPrevious: page > 1,
		HasNext:     hasMore,
	}, nil
}

func (s *Store) IDs() (map[string]struct{}, error) {
	if s == nil {
		return nil, errors.New("audit log store is nil")
	}
	paths, err := s.listFilePaths()
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{})
	for _, path := range paths {
		records, err := s.readFileRecords(path)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			ids[record.ID] = struct{}{}
		}
	}
	return ids, nil
}

func (s *Store) Purge(ownerID string, retainDays int, now time.Time) (PurgeResult, error) {
	if s == nil {
		return PurgeResult{}, errors.New("audit log store is nil")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return PurgeResult{}, errors.New("audit log owner ID is required")
	}
	if retainDays < 1 {
		return PurgeResult{}, errors.New("audit log retention days must be positive")
	}
	if now.IsZero() {
		now = time.Now()
	}
	keepFrom := keepFromDate(now, retainDays)
	s.mu.Lock()
	defer s.mu.Unlock()
	paths, err := s.listFilePaths()
	if err != nil {
		return PurgeResult{}, err
	}
	var result PurgeResult
	for _, path := range paths {
		day, ok := auditFileDate(path)
		if !ok || !day.Before(keepFrom) {
			continue
		}
		removed, deleted, err := s.purgeOwnerFromFile(path, ownerID)
		if err != nil {
			return result, err
		}
		result.RemovedRecords += removed
		if deleted {
			result.RemovedFiles++
		} else if removed > 0 {
			result.RewrittenFiles++
		}
	}
	return result, nil
}

func (s *Store) listFilePaths() ([]string, error) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, fmt.Errorf("list audit log files: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix) {
			paths = append(paths, filepath.Join(s.directory, name))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	return paths, nil
}

func (s *Store) collectOwnerRecordsFromEnd(path, ownerID string, seen map[string]struct{}, limit int) ([]Record, error) {
	if limit <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open audit log file for reading: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat audit log file: %w", err)
	}
	size := info.Size()
	if size == 0 {
		return nil, nil
	}
	lastByte := []byte{0}
	if _, err := file.ReadAt(lastByte, size-1); err != nil {
		return nil, fmt.Errorf("inspect audit log file ending: %w", err)
	}
	endsWithNewline := lastByte[0] == '\n'
	collected := make([]Record, 0, limit)
	var leftover []byte
	offset := size
	newestFragment := true
	for offset > 0 && len(collected) < limit {
		chunk := int64(reverseReadChunk)
		if chunk > offset {
			chunk = offset
		}
		offset -= chunk
		buf := make([]byte, chunk)
		if _, err := file.ReadAt(buf, offset); err != nil {
			return nil, fmt.Errorf("read audit log file: %w", err)
		}
		buf = append(buf, leftover...)
		parts := bytes.Split(buf, []byte{'\n'})
		var complete [][]byte
		if offset > 0 {
			leftover = append([]byte(nil), parts[0]...)
			if len(leftover) > maxRecordBytes+1 {
				return nil, fmt.Errorf("audit log file %s exceeds %d byte limit", filepath.Base(path), maxRecordBytes)
			}
			complete = parts[1:]
		} else {
			leftover = nil
			complete = parts
		}
		for i := len(complete) - 1; i >= 0; i-- {
			allowTorn := newestFragment && !endsWithNewline
			newestFragment = false
			record, skip, err := decodeAuditLine(path, complete[i], allowTorn)
			if err != nil {
				return nil, err
			}
			if skip {
				continue
			}
			if ownerID != "" && record.OwnerID != ownerID {
				continue
			}
			if _, duplicate := seen[record.ID]; duplicate {
				continue
			}
			seen[record.ID] = struct{}{}
			collected = append(collected, record)
			if len(collected) >= limit {
				break
			}
		}
	}
	return collected, nil
}

func (s *Store) readFileRecords(path string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open audit log file for reading: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat audit log file: %w", err)
	}
	endsWithNewline := true
	if info.Size() > 0 {
		lastByte := []byte{0}
		if _, err := file.ReadAt(lastByte, info.Size()-1); err != nil {
			file.Close()
			return nil, fmt.Errorf("inspect audit log file ending: %w", err)
		}
		endsWithNewline = lastByte[0] == '\n'
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxRecordBytes+1)
	var pending []byte
	pendingLine := 0
	lineNumber := 0
	records := make([]Record, 0)
	appendLine := func(raw []byte, number int, allowTorn bool) error {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			return nil
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			if allowTorn {
				return nil
			}
			return fmt.Errorf("decode audit log file %s line %d: %w", filepath.Base(path), number, err)
		}
		if record.ID == "" || record.OwnerID == "" {
			return fmt.Errorf("audit log file %s line %d is missing id or owner_id", filepath.Base(path), number)
		}
		if err := validateRecord(record); err != nil {
			return fmt.Errorf("audit log file %s line %d is invalid: %w", filepath.Base(path), number, err)
		}
		records = append(records, record)
		return nil
	}
	for scanner.Scan() {
		lineNumber++
		if pending != nil {
			if err := appendLine(pending, pendingLine, false); err != nil {
				file.Close()
				return nil, err
			}
		}
		pending = append(pending[:0], scanner.Bytes()...)
		pendingLine = lineNumber
	}
	scanErr := scanner.Err()
	if scanErr == nil && pending != nil {
		scanErr = appendLine(pending, pendingLine, !endsWithNewline)
	}
	closeErr := file.Close()
	if scanErr != nil {
		return nil, fmt.Errorf("read audit log file: %w", scanErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close audit log file: %w", closeErr)
	}
	return records, nil
}

func decodeAuditLine(path string, raw []byte, allowTorn bool) (Record, bool, error) {
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return Record{}, true, nil
	}
	if len(line) > maxRecordBytes {
		return Record{}, false, fmt.Errorf("audit log file %s exceeds %d byte limit", filepath.Base(path), maxRecordBytes)
	}
	var record Record
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		if allowTorn {
			return Record{}, true, nil
		}
		return Record{}, false, fmt.Errorf("decode audit log file %s: %w", filepath.Base(path), err)
	}
	if record.ID == "" || record.OwnerID == "" {
		return Record{}, false, fmt.Errorf("audit log file %s is missing id or owner_id", filepath.Base(path))
	}
	if err := validateRecord(record); err != nil {
		return Record{}, false, fmt.Errorf("audit log file %s is invalid: %w", filepath.Base(path), err)
	}
	return record, false, nil
}

func (s *Store) purgeOwnerFromFile(path, ownerID string) (int, bool, error) {
	records, err := s.readFileRecords(path)
	if err != nil {
		return 0, false, err
	}
	kept := records[:0]
	removed := 0
	for _, record := range records {
		if record.OwnerID == ownerID {
			removed++
			continue
		}
		kept = append(kept, record)
	}
	if removed == 0 {
		return 0, false, nil
	}
	if err := s.replaceFileRecords(path, kept); err != nil {
		return 0, false, err
	}
	return removed, len(kept) == 0, nil
}

func (s *Store) replaceFileRecords(path string, records []Record) error {
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove purged audit log file: %w", err)
		}
		return nil
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create purged audit log file: %w", err)
	}
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			file.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("encode purged audit event: %w", err)
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			file.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("write purged audit log file: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close purged audit log file: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace audit log file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace audit log file: %w", err)
	}
	return nil
}

func auditFileDate(path string) (time.Time, bool) {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
		return time.Time{}, false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
	day, err := time.Parse("2006-01-02", stamp)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

func keepFromDate(now time.Time, retainDays int) time.Time {
	utc := now.UTC()
	today := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	return today.AddDate(0, 0, -retainDays+1)
}

func validateRecord(record Record) error {
	if strings.TrimSpace(record.ID) == "" {
		return errors.New("audit event ID is required")
	}
	if strings.TrimSpace(record.OwnerID) == "" {
		return errors.New("audit event owner ID is required")
	}
	if strings.TrimSpace(record.Action) == "" {
		return errors.New("audit event action is required")
	}
	if strings.TrimSpace(record.Outcome) == "" {
		return errors.New("audit event outcome is required")
	}
	if record.Outcome != "success" && record.Outcome != "failed" && record.Outcome != "skipped" {
		return errors.New("audit event outcome is invalid")
	}
	if record.CreatedAt.IsZero() {
		return errors.New("audit event creation time is required")
	}
	return nil
}
