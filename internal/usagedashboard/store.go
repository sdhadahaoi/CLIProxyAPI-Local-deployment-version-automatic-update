package usagedashboard

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	recordsPrefix     = "usage-"
	recordsSuffix     = ".jsonl"
	priceFileName      = "prices.json"
	defaultQueryLimit  = 200
	maxQueryLimit      = 2000
	defaultQueryWindow = 24 * time.Hour
)

func init() {
	coreusage.RegisterPlugin(usagePlugin{})
}

type usagePlugin struct{}

func (usagePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	currentStore().handleUsage(ctx, record)
}

type Store struct {
	mu        sync.RWMutex
	enabled   bool
	dataDir   string
	prices    PriceBook
	persister usagePersister

	syncMu   sync.Mutex
	dirty    map[string]struct{}
	syncing  bool
	lastSync time.Time
}

type usagePersister interface {
	PersistAuthFiles(context.Context, string, ...string) error
}

type PriceBook struct {
	Currency  string                `json:"currency"`
	Custom    map[string]ModelPrice `json:"custom"`
	Simulated map[string]ModelPrice `json:"simulated"`
	UpdatedAt time.Time             `json:"updated_at"`
}

type ModelPrice struct {
	InputPerMillion     float64 `json:"input_per_million"`
	OutputPerMillion    float64 `json:"output_per_million"`
	ReasoningPerMillion float64 `json:"reasoning_per_million"`
	CachedPerMillion    float64 `json:"cached_per_million"`
}

type TokenTotals struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type Record struct {
	Timestamp       time.Time   `json:"timestamp"`
	RequestID       string      `json:"request_id,omitempty"`
	Provider        string      `json:"provider"`
	ExecutorType    string      `json:"executor_type,omitempty"`
	Model           string      `json:"model"`
	Alias           string      `json:"alias"`
	Endpoint        string      `json:"endpoint,omitempty"`
	Source          string      `json:"source,omitempty"`
	AuthID          string      `json:"auth_id,omitempty"`
	AuthIndex       string      `json:"auth_index,omitempty"`
	AuthType        string      `json:"auth_type,omitempty"`
	APIKeyHash      string      `json:"api_key_hash,omitempty"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
	ServiceTier     string      `json:"service_tier,omitempty"`
	LatencyMs       int64       `json:"latency_ms"`
	TTFTMs          int64       `json:"ttft_ms"`
	Tokens          TokenTotals `json:"tokens"`
	Failed          bool        `json:"failed"`
	StatusCode      int         `json:"status_code"`
	FailureBody     string      `json:"failure_body,omitempty"`
}

type PricedRecord struct {
	Record
	CustomCost    float64 `json:"custom_cost"`
	SimulatedCost float64 `json:"simulated_cost"`
}

type Bucket struct {
	Key           string      `json:"key"`
	Requests      int64       `json:"requests"`
	Failed        int64       `json:"failed"`
	Tokens        TokenTotals `json:"tokens"`
	CustomCost    float64     `json:"custom_cost"`
	SimulatedCost float64     `json:"simulated_cost"`
}

type Summary struct {
	Enabled bool           `json:"enabled"`
	DataDir string         `json:"data_dir"`
	From    time.Time      `json:"from"`
	To      time.Time      `json:"to"`
	Prices  PriceBook      `json:"prices"`
	Total   Bucket         `json:"total"`
	ByModel []Bucket       `json:"by_model"`
	ByDay   []Bucket       `json:"by_day"`
	Records []PricedRecord `json:"records"`
}

type Query struct {
	From     time.Time
	To       time.Time
	Model    string
	Provider string
	Limit    int
}

type UsageFile struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

type MergeResult struct {
	Name       string `json:"name"`
	Action     string `json:"action"`
	RemoteSize int64  `json:"remote_size"`
	Records    int    `json:"records,omitempty"`
}

var globalStore = &Store{prices: defaultPriceBook()}

func Configure(cfg internalconfig.UsageDashboardConfig, authDir string, persister usagePersister) error {
	cfg = resolveConfigFromEnv(cfg)
	globalStore.mu.Lock()
	defer globalStore.mu.Unlock()

	globalStore.enabled = cfg.Enabled
	globalStore.dataDir = strings.TrimSpace(cfg.DataDir)
	globalStore.persister = persister
	if !globalStore.enabled {
		return nil
	}
	if globalStore.dataDir == "" {
		globalStore.dataDir = defaultDataDir(authDir)
	}
	absDir, errAbs := filepath.Abs(globalStore.dataDir)
	if errAbs == nil {
		globalStore.dataDir = absDir
	}
	if errMkdir := os.MkdirAll(globalStore.dataDir, 0755); errMkdir != nil {
		globalStore.enabled = false
		return fmt.Errorf("create usage dashboard data dir: %w", errMkdir)
	}
	prices, errPrices := loadOrCreatePriceBookLocked(globalStore.dataDir)
	if errPrices != nil {
		return errPrices
	}
	globalStore.prices = prices
	return nil
}

func defaultDataDir(authDir string) string {
	authDir = strings.TrimSpace(authDir)
	if authDir != "" {
		if abs, errAbs := filepath.Abs(authDir); errAbs == nil {
			authDir = abs
		}
		parent := filepath.Dir(authDir)
		if parent != "" && parent != "." {
			return filepath.Join(parent, "usage-dashboard")
		}
	}
	return "usage-dashboard-data"
}

func Enabled() bool {
	globalStore.mu.RLock()
	defer globalStore.mu.RUnlock()
	return globalStore.enabled
}

func DataDir() string {
	globalStore.mu.RLock()
	defer globalStore.mu.RUnlock()
	return globalStore.dataDir
}

func Prices() PriceBook {
	globalStore.mu.RLock()
	defer globalStore.mu.RUnlock()
	return clonePriceBook(globalStore.prices)
}

func SavePrices(book PriceBook) (PriceBook, error) {
	globalStore.mu.Lock()
	defer globalStore.mu.Unlock()
	if !globalStore.enabled {
		return PriceBook{}, errors.New("usage dashboard is disabled")
	}
	book = normalizePriceBook(book)
	if err := writeJSONFile(filepath.Join(globalStore.dataDir, priceFileName), book); err != nil {
		return PriceBook{}, err
	}
	globalStore.prices = book
	globalStore.markDirtyLocked(filepath.Join(globalStore.dataDir, priceFileName))
	return clonePriceBook(book), nil
}

func Summarize(query Query) (Summary, error) {
	store := currentStore()
	store.mu.RLock()
	enabled := store.enabled
	dataDir := store.dataDir
	prices := clonePriceBook(store.prices)
	store.mu.RUnlock()

	query = normalizeQuery(query)
	summary := Summary{
		Enabled: enabled,
		DataDir: dataDir,
		From:    query.From,
		To:      query.To,
		Prices:  prices,
		Total:   Bucket{Key: "total"},
	}
	if !enabled {
		return summary, nil
	}

	modelBuckets := make(map[string]*Bucket)
	dayBuckets := make(map[string]*Bucket)
	err := scanRecords(dataDir, query.From, query.To, func(record Record) {
		if query.Model != "" && !strings.EqualFold(record.Model, query.Model) && !strings.EqualFold(record.Alias, query.Model) {
			return
		}
		if query.Provider != "" && !strings.EqualFold(record.Provider, query.Provider) {
			return
		}
		priced := PricedRecord{
			Record:        record,
			CustomCost:    calculateCost(record.Tokens, record.Model, record.Alias, prices.Custom),
			SimulatedCost: calculateCost(record.Tokens, record.Model, record.Alias, prices.Simulated),
		}
		addPricedRecord(&summary.Total, priced)

		modelKey := record.Model
		if modelKey == "" {
			modelKey = "unknown"
		}
		modelBucket := bucketFor(modelBuckets, modelKey)
		addPricedRecord(modelBucket, priced)

		dayKey := record.Timestamp.UTC().Format("2006-01-02")
		dayBucket := bucketFor(dayBuckets, dayKey)
		addPricedRecord(dayBucket, priced)

		addRecentRecord(&summary.Records, priced, query.Limit)
	})
	if err != nil {
		return summary, err
	}
	sort.Slice(summary.Records, func(i, j int) bool {
		return summary.Records[i].Timestamp.After(summary.Records[j].Timestamp)
	})
	summary.ByModel = flattenBuckets(modelBuckets)
	sort.Slice(summary.ByModel, func(i, j int) bool {
		return summary.ByModel[i].Tokens.TotalTokens > summary.ByModel[j].Tokens.TotalTokens
	})
	summary.ByDay = flattenBuckets(dayBuckets)
	sort.Slice(summary.ByDay, func(i, j int) bool {
		return summary.ByDay[i].Key < summary.ByDay[j].Key
	})
	return summary, nil
}

func ListFiles() ([]UsageFile, error) {
	globalStore.mu.RLock()
	enabled := globalStore.enabled
	dataDir := globalStore.dataDir
	globalStore.mu.RUnlock()
	if !enabled {
		return nil, errors.New("usage dashboard is disabled")
	}
	entries, errRead := os.ReadDir(dataDir)
	if errRead != nil {
		return nil, errRead
	}
	files := make([]UsageFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isUsageFileName(name) && name != priceFileName {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		files = append(files, UsageFile{Name: name, Size: info.Size(), ModTime: info.ModTime().UTC()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func OpenFile(name string) (*os.File, string, error) {
	globalStore.mu.RLock()
	enabled := globalStore.enabled
	dataDir := globalStore.dataDir
	globalStore.mu.RUnlock()
	if !enabled {
		return nil, "", errors.New("usage dashboard is disabled")
	}
	name = filepath.Base(strings.TrimSpace(name))
	if !isUsageFileName(name) && name != priceFileName {
		return nil, "", fmt.Errorf("invalid usage dashboard file name")
	}
	path := filepath.Join(dataDir, name)
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, "", errOpen
	}
	contentType := "application/json"
	if strings.HasSuffix(name, recordsSuffix) {
		contentType = "application/x-ndjson"
	}
	return file, contentType, nil
}

func MergeUploadedFile(name string, reader io.Reader) (MergeResult, error) {
	globalStore.mu.Lock()
	defer globalStore.mu.Unlock()
	if !globalStore.enabled {
		return MergeResult{}, errors.New("usage dashboard is disabled")
	}
	name = filepath.Base(strings.TrimSpace(name))
	if !isUsageFileName(name) && name != priceFileName {
		return MergeResult{}, fmt.Errorf("invalid usage dashboard file name")
	}
	if errMkdir := os.MkdirAll(globalStore.dataDir, 0755); errMkdir != nil {
		return MergeResult{}, errMkdir
	}
	if name == priceFileName {
		return mergeUploadedPricesLocked(name, reader)
	}
	return mergeUploadedRecordsLocked(name, reader)
}

func currentStore() *Store { return globalStore }

func (s *Store) handleUsage(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	s.mu.RLock()
	enabled := s.enabled
	dataDir := s.dataDir
	s.mu.RUnlock()
	if !enabled {
		return
	}

	out := recordFromUsage(ctx, record)
	payload, errMarshal := json.Marshal(out)
	if errMarshal != nil {
		log.WithError(errMarshal).Debug("failed to encode usage dashboard record")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	if dataDir != s.dataDir {
		dataDir = s.dataDir
	}
	if errMkdir := os.MkdirAll(dataDir, 0755); errMkdir != nil {
		log.WithError(errMkdir).Warn("failed to create usage dashboard data dir")
		return
	}
	path := filepath.Join(dataDir, recordsFileName(out.Timestamp))
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if errOpen != nil {
		log.WithError(errOpen).Warn("failed to open usage dashboard record file")
		return
	}
	if _, errWrite := file.Write(append(payload, '\n')); errWrite != nil {
		log.WithError(errWrite).Warn("failed to append usage dashboard record")
	}
	if errClose := file.Close(); errClose != nil {
		log.WithError(errClose).Debug("failed to close usage dashboard record file")
	}
	s.markDirtyLocked(path)
}

func recordFromUsage(ctx context.Context, record coreusage.Record) Record {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	model := strings.TrimSpace(record.Model)
	if model == "" {
		model = "unknown"
	}
	alias := strings.TrimSpace(record.Alias)
	if alias == "" {
		alias = model
	}
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	status := record.Fail.StatusCode
	if status <= 0 {
		status = internallogging.GetResponseStatus(ctx)
	}
	failed := record.Failed
	if !failed && status >= http.StatusBadRequest {
		failed = true
	}
	if status <= 0 {
		if failed {
			status = http.StatusInternalServerError
		} else {
			status = http.StatusOK
		}
	}
	tokens := TokenTotals{
		InputTokens:         record.Detail.InputTokens,
		OutputTokens:        record.Detail.OutputTokens,
		ReasoningTokens:     record.Detail.ReasoningTokens,
		CachedTokens:        record.Detail.CachedTokens,
		CacheReadTokens:     record.Detail.CacheReadTokens,
		CacheCreationTokens: record.Detail.CacheCreationTokens,
		TotalTokens:         record.Detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}
	reasoningEffort := strings.TrimSpace(record.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = coreusage.ReasoningEffortFromContext(ctx)
	}
	serviceTier := strings.TrimSpace(record.ServiceTier)
	if serviceTier == "" {
		serviceTier = coreusage.ServiceTierFromContext(ctx)
	}
	return Record{
		Timestamp:       timestamp.UTC(),
		RequestID:       strings.TrimSpace(internallogging.GetRequestID(ctx)),
		Provider:        provider,
		ExecutorType:    strings.TrimSpace(record.ExecutorType),
		Model:           model,
		Alias:           alias,
		Endpoint:        strings.TrimSpace(internallogging.GetEndpoint(ctx)),
		Source:          strings.TrimSpace(record.Source),
		AuthID:          strings.TrimSpace(record.AuthID),
		AuthIndex:       strings.TrimSpace(record.AuthIndex),
		AuthType:        strings.TrimSpace(record.AuthType),
		APIKeyHash:      apiKeyHash(record.APIKey),
		ReasoningEffort: reasoningEffort,
		ServiceTier:     serviceTier,
		LatencyMs:       record.Latency.Milliseconds(),
		TTFTMs:          record.TTFT.Milliseconds(),
		Tokens:          tokens,
		Failed:          failed,
		StatusCode:      status,
		FailureBody:     strings.TrimSpace(record.Fail.Body),
	}
}

func apiKeyHash(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])[:16]
}

func resolveConfigFromEnv(cfg internalconfig.UsageDashboardConfig) internalconfig.UsageDashboardConfig {
	if raw := strings.TrimSpace(os.Getenv("USAGE_DASHBOARD_ENABLED")); raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			cfg.Enabled = true
		case "0", "false", "no", "off":
			cfg.Enabled = false
		}
	}
	if dir := strings.TrimSpace(os.Getenv("USAGE_DASHBOARD_DIR")); dir != "" {
		cfg.DataDir = dir
	}
	return cfg
}

func loadOrCreatePriceBookLocked(dataDir string) (PriceBook, error) {
	path := filepath.Join(dataDir, priceFileName)
	data, errRead := os.ReadFile(path)
	if errRead == nil {
		var book PriceBook
		if errUnmarshal := json.Unmarshal(data, &book); errUnmarshal != nil {
			return PriceBook{}, fmt.Errorf("parse usage dashboard prices: %w", errUnmarshal)
		}
		return normalizePriceBook(book), nil
	}
	if !os.IsNotExist(errRead) {
		return PriceBook{}, errRead
	}
	book := defaultPriceBook()
	if errWrite := writeJSONFile(path, book); errWrite != nil {
		return PriceBook{}, errWrite
	}
	return book, nil
}

func defaultPriceBook() PriceBook {
	now := time.Now().UTC()
	return PriceBook{
		Currency:  "USD",
		Custom:    map[string]ModelPrice{},
		Simulated: map[string]ModelPrice{},
		UpdatedAt: now,
	}
}

func normalizePriceBook(book PriceBook) PriceBook {
	book.Currency = strings.ToUpper(strings.TrimSpace(book.Currency))
	if book.Currency == "" {
		book.Currency = "USD"
	}
	book.Custom = normalizePriceMap(book.Custom)
	book.Simulated = normalizePriceMap(book.Simulated)
	book.UpdatedAt = time.Now().UTC()
	return book
}

func normalizePriceMap(in map[string]ModelPrice) map[string]ModelPrice {
	out := make(map[string]ModelPrice, len(in))
	for key, price := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = sanitizePrice(price)
	}
	return out
}

func sanitizePrice(price ModelPrice) ModelPrice {
	if price.InputPerMillion < 0 {
		price.InputPerMillion = 0
	}
	if price.OutputPerMillion < 0 {
		price.OutputPerMillion = 0
	}
	if price.ReasoningPerMillion < 0 {
		price.ReasoningPerMillion = 0
	}
	if price.CachedPerMillion < 0 {
		price.CachedPerMillion = 0
	}
	return price
}

func clonePriceBook(book PriceBook) PriceBook {
	out := book
	out.Custom = clonePriceMap(book.Custom)
	out.Simulated = clonePriceMap(book.Simulated)
	return out
}

func clonePriceMap(in map[string]ModelPrice) map[string]ModelPrice {
	out := make(map[string]ModelPrice, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func writeJSONFile(path string, value any) error {
	data, errMarshal := json.MarshalIndent(value, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0644); errWrite != nil {
		return errWrite
	}
	return replaceFile(tmp, path)
}

func replaceFile(tmp, path string) error {
	if errRename := os.Rename(tmp, path); errRename == nil {
		return nil
	}
	if errRemove := os.Remove(path); errRemove != nil && !os.IsNotExist(errRemove) {
		return errRemove
	}
	return os.Rename(tmp, path)
}

func mergeUploadedPricesLocked(name string, reader io.Reader) (MergeResult, error) {
	data, errRead := io.ReadAll(io.LimitReader(reader, 4*1024*1024))
	if errRead != nil {
		return MergeResult{}, errRead
	}
	var uploaded PriceBook
	if errUnmarshal := json.Unmarshal(data, &uploaded); errUnmarshal != nil {
		return MergeResult{}, errUnmarshal
	}
	current := globalStore.prices
	action := "kept_remote"
	if uploaded.UpdatedAt.IsZero() || current.UpdatedAt.IsZero() || uploaded.UpdatedAt.After(current.UpdatedAt) {
		uploaded = normalizePriceBook(uploaded)
		if errWrite := writeJSONFile(filepath.Join(globalStore.dataDir, name), uploaded); errWrite != nil {
			return MergeResult{}, errWrite
		}
		globalStore.prices = uploaded
		globalStore.markDirtyLocked(filepath.Join(globalStore.dataDir, name))
		action = "replaced_remote"
	}
	info, _ := os.Stat(filepath.Join(globalStore.dataDir, name))
	result := MergeResult{Name: name, Action: action}
	if info != nil {
		result.RemoteSize = info.Size()
	}
	return result, nil
}

func mergeUploadedRecordsLocked(name string, reader io.Reader) (MergeResult, error) {
	path := filepath.Join(globalStore.dataDir, name)
	records := make(map[string]Record)
	if file, errOpen := os.Open(path); errOpen == nil {
		_ = scanRecordFile(file, time.Time{}, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), func(record Record) {
			records[recordDedupKey(record)] = record
		})
		_ = file.Close()
	} else if !os.IsNotExist(errOpen) {
		return MergeResult{}, errOpen
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if errUnmarshal := json.Unmarshal([]byte(line), &record); errUnmarshal != nil {
			continue
		}
		if record.Timestamp.IsZero() {
			continue
		}
		record.Timestamp = record.Timestamp.UTC()
		records[recordDedupKey(record)] = record
	}
	if errScan := scanner.Err(); errScan != nil {
		return MergeResult{}, errScan
	}

	out := make([]Record, 0, len(records))
	for _, record := range records {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	tmp := path + ".tmp"
	file, errCreate := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if errCreate != nil {
		return MergeResult{}, errCreate
	}
	writer := bufio.NewWriter(file)
	for _, record := range out {
		payload, errMarshal := json.Marshal(record)
		if errMarshal != nil {
			_ = file.Close()
			_ = os.Remove(tmp)
			return MergeResult{}, errMarshal
		}
		if _, errWrite := writer.Write(append(payload, '\n')); errWrite != nil {
			_ = file.Close()
			_ = os.Remove(tmp)
			return MergeResult{}, errWrite
		}
	}
	if errFlush := writer.Flush(); errFlush != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return MergeResult{}, errFlush
	}
	if errClose := file.Close(); errClose != nil {
		_ = os.Remove(tmp)
		return MergeResult{}, errClose
	}
	if errRename := replaceFile(tmp, path); errRename != nil {
		_ = os.Remove(tmp)
		return MergeResult{}, errRename
	}
	globalStore.markDirtyLocked(path)
	info, _ := os.Stat(path)
	result := MergeResult{Name: name, Action: "merged", Records: len(out)}
	if info != nil {
		result.RemoteSize = info.Size()
	}
	return result, nil
}

func recordDedupKey(record Record) string {
	if record.RequestID != "" {
		return "request:" + record.RequestID + "|" + record.Provider + "|" + record.Model + "|" + record.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	payload, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		return fmt.Sprintf("%s|%s|%s|%d|%d|%d", record.Timestamp.UTC().Format(time.RFC3339Nano), record.RequestID, record.Model, record.Tokens.InputTokens, record.Tokens.OutputTokens, record.Tokens.TotalTokens)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func normalizeQuery(query Query) Query {
	query.Model = strings.TrimSpace(query.Model)
	query.Provider = strings.TrimSpace(query.Provider)
	if query.To.IsZero() {
		query.To = time.Now().UTC()
	}
	if query.From.IsZero() {
		query.From = query.To.Add(-defaultQueryWindow)
	}
	query.From = query.From.UTC()
	query.To = query.To.UTC()
	if query.From.After(query.To) {
		query.From, query.To = query.To, query.From
	}
	if query.Limit <= 0 {
		query.Limit = defaultQueryLimit
	}
	if query.Limit > maxQueryLimit {
		query.Limit = maxQueryLimit
	}
	return query
}

func scanRecords(dataDir string, from, to time.Time, fn func(Record)) error {
	for day := startOfDay(from); !day.After(to); day = day.AddDate(0, 0, 1) {
		path := filepath.Join(dataDir, recordsFileName(day))
		file, errOpen := os.Open(path)
		if errOpen != nil {
			if os.IsNotExist(errOpen) {
				continue
			}
			return errOpen
		}
		if errScan := scanRecordFile(file, from, to, fn); errScan != nil {
			_ = file.Close()
			return errScan
		}
		if errClose := file.Close(); errClose != nil {
			return errClose
		}
	}
	return nil
}

func scanRecordFile(reader io.Reader, from, to time.Time, fn func(Record)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if errUnmarshal := json.Unmarshal([]byte(line), &record); errUnmarshal != nil {
			continue
		}
		if record.Timestamp.Before(from) || record.Timestamp.After(to) {
			continue
		}
		fn(record)
	}
	return scanner.Err()
}

func recordsFileName(t time.Time) string {
	return recordsPrefix + t.UTC().Format("2006-01-02") + recordsSuffix
}

func isUsageFileName(name string) bool {
	if !strings.HasPrefix(name, recordsPrefix) || !strings.HasSuffix(name, recordsSuffix) {
		return false
	}
	day := strings.TrimSuffix(strings.TrimPrefix(name, recordsPrefix), recordsSuffix)
	_, errParse := time.Parse("2006-01-02", day)
	return errParse == nil
}

func startOfDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func calculateCost(tokens TokenTotals, model, alias string, prices map[string]ModelPrice) float64 {
	price, ok := priceForModel(model, alias, prices)
	if !ok {
		return 0
	}
	input := float64(tokens.InputTokens) * price.InputPerMillion / 1_000_000
	output := float64(tokens.OutputTokens) * price.OutputPerMillion / 1_000_000
	reasoning := float64(tokens.ReasoningTokens) * price.ReasoningPerMillion / 1_000_000
	cachedTokens := tokens.CachedTokens + tokens.CacheReadTokens + tokens.CacheCreationTokens
	cached := float64(cachedTokens) * price.CachedPerMillion / 1_000_000
	return input + output + reasoning + cached
}

func priceForModel(model, alias string, prices map[string]ModelPrice) (ModelPrice, bool) {
	candidates := []string{model, alias, strings.ToLower(model), strings.ToLower(alias), "default"}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if price, ok := prices[candidate]; ok {
			return price, true
		}
	}
	return ModelPrice{}, false
}

func bucketFor(buckets map[string]*Bucket, key string) *Bucket {
	if bucket, ok := buckets[key]; ok {
		return bucket
	}
	bucket := &Bucket{Key: key}
	buckets[key] = bucket
	return bucket
}

func addPricedRecord(bucket *Bucket, record PricedRecord) {
	bucket.Requests++
	if record.Failed {
		bucket.Failed++
	}
	addTokens(&bucket.Tokens, record.Tokens)
	bucket.CustomCost += record.CustomCost
	bucket.SimulatedCost += record.SimulatedCost
}

func addTokens(dst *TokenTotals, src TokenTotals) {
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.CachedTokens += src.CachedTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.CacheCreationTokens += src.CacheCreationTokens
	dst.TotalTokens += src.TotalTokens
}

func flattenBuckets(in map[string]*Bucket) []Bucket {
	out := make([]Bucket, 0, len(in))
	for _, bucket := range in {
		out = append(out, *bucket)
	}
	return out
}

func addRecentRecord(records *[]PricedRecord, record PricedRecord, limit int) {
	if records == nil || limit <= 0 {
		return
	}
	if len(*records) < limit {
		*records = append(*records, record)
		return
	}
	oldest := 0
	for i := 1; i < len(*records); i++ {
		if (*records)[i].Timestamp.Before((*records)[oldest].Timestamp) {
			oldest = i
		}
	}
	if record.Timestamp.After((*records)[oldest].Timestamp) {
		(*records)[oldest] = record
	}
}

func (s *Store) markDirtyLocked(path string) {
	path = strings.TrimSpace(path)
	if s == nil || path == "" || s.persister == nil {
		return
	}
	s.syncMu.Lock()
	if s.dirty == nil {
		s.dirty = make(map[string]struct{})
	}
	s.dirty[path] = struct{}{}
	if s.syncing || time.Since(s.lastSync) < 30*time.Second {
		s.syncMu.Unlock()
		return
	}
	s.syncing = true
	s.syncMu.Unlock()
	go s.flushDirty(context.Background())
}

func (s *Store) flushDirty(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("usage dashboard: git sync panic recovered: %v", r)
		}
	}()
	time.Sleep(2 * time.Second)
	s.syncMu.Lock()
	paths := make([]string, 0, len(s.dirty))
	for path := range s.dirty {
		paths = append(paths, path)
	}
	s.dirty = make(map[string]struct{})
	s.lastSync = time.Now()
	s.syncing = false
	persister := s.persister
	s.syncMu.Unlock()
	if len(paths) == 0 || persister == nil {
		return
	}
	if err := persister.PersistAuthFiles(ctx, "Update usage dashboard data", paths...); err != nil {
		log.WithError(err).Warn("failed to sync usage dashboard data to backing store")
		s.syncMu.Lock()
		if s.dirty == nil {
			s.dirty = make(map[string]struct{})
		}
		for _, path := range paths {
			s.dirty[path] = struct{}{}
		}
		s.syncMu.Unlock()
	}
}
