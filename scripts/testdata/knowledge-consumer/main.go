// This executable is compiled inside the pinned consumer module, not LLMRouter.
// See README.md for its deliberately bounded fixture contract.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	sqliteretriever "github.com/Tencent/WeKnora/internal/application/repository/retriever/sqlite"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/models/api/dashscoperank"
	"github.com/Tencent/WeKnora/internal/models/api/openaiembeddings"
	"github.com/Tencent/WeKnora/internal/sourceloc"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const consumerPin = "f5adb9eac166e7d3da7dceed7aa50049d4c4db1f"
const vectorWidth = 1024
const query = "阀门维护规范"

var pageText = []string{
	"冷却规范：阀门必须关闭。 Cooling manual specifies valve closure.",
	"检修规范：阀门检查周期为七天。 Maintenance interval is seven days.",
}

type chunkEvidence struct {
	ChunkID        string               `json:"chunk_id"`
	Content        string               `json:"content"`
	Start          int                  `json:"start"`
	End            int                  `json:"end"`
	SourceLocators types.SourceLocators `json:"source_locators"`
}

type imageEvidence struct {
	Filename    string `json:"filename"`
	OriginalRef string `json:"original_ref"`
	MIME        string `json:"mime"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
}

type summary struct {
	SchemaVersion    int                     `json:"schema_version"`
	ConsumerPin      string                  `json:"consumer_pin"`
	BuildRevision    string                  `json:"build_revision"`
	Scope            string                  `json:"scope"`
	DocumentSHA256   string                  `json:"document_sha256"`
	Markdown         string                  `json:"markdown"`
	SourceBlocks     []types.SourceBlock     `json:"source_blocks"`
	Images           []imageEvidence         `json:"images"`
	Chunks           []chunkEvidence         `json:"chunks"`
	VectorWidth      int                     `json:"vector_width"`
	DocumentWidths   []int                   `json:"document_vector_widths"`
	QueryWidth       int                     `json:"query_vector_width"`
	VectorTableSQL   string                  `json:"vector_table_sql"`
	FTSTableSQL      string                  `json:"fts_table_sql"`
	VectorResults    []*types.RetrieveResult `json:"vector_results"`
	KeywordResults   []*types.RetrieveResult `json:"keyword_results"`
	Candidates       []*types.IndexWithScore `json:"candidates"`
	RerankResults    []api.RerankResult      `json:"rerank_results"`
	WinningIndex     int                     `json:"winning_rerank_index"`
	WinningScore     float64                 `json:"winning_rerank_score"`
	Winner           chunkEvidence           `json:"winner"`
	WinnerVectorHit  bool                    `json:"winner_vector_hit"`
	WinnerKeywordHit bool                    `json:"winner_keyword_hit"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "knowledge-consumer:", err)
		os.Exit(1)
	}
}

func run() error {
	gateway := flag.String("gateway-url", "", "ephemeral fixture http://127.0.0.1:<port> origin")
	token := flag.String("platform-token", "", "ephemeral fixture platform token")
	database := flag.String("database", "", "new absolute SQLite file in the OS temporary directory")
	output := flag.String("summary", "", "new absolute summary JSON file")
	document := flag.String("documentpath", "", "generated two-page fixture PDF")
	flag.Parse()
	u, err := url.Parse(*gateway)
	if err != nil {
		return fmt.Errorf("invalid fixture gateway URL: %w", err)
	}
	port, portErr := strconv.Atoi(u.Port())
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || portErr != nil || port < 1024 || port > 65535 || port == 17808 {
		return fmt.Errorf("gateway must be a literal http://127.0.0.1:<ephemeral-port> origin, excluding port 17808")
	}
	if *token == "" || flag.NArg() != 0 {
		return fmt.Errorf("fixture platform token required; positional arguments are unsupported")
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("missing consumer build provenance")
	}
	var revision string
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
	}
	if revision != consumerPin {
		return fmt.Errorf("consumer build revision %q must equal %s (build with VCS stamping)", revision, consumerPin)
	}
	if !filepath.IsAbs(*database) || !filepath.IsAbs(*output) || filepath.Ext(*database) != ".sqlite" || strings.ContainsAny(*database, "?\x00") {
		return fmt.Errorf("database must be an absolute new .sqlite path; summary must be absolute")
	}
	tempRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return err
	}
	dbParent, err := filepath.EvalSymlinks(filepath.Dir(*database))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(tempRoot, dbParent)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("database must be inside a dedicated temporary subdirectory")
	}
	fixture, err := os.ReadFile(*document)
	if err != nil {
		return fmt.Errorf("read fixture PDF: %w", err)
	}
	if !bytes.HasPrefix(fixture, []byte("%PDF-")) || len(fixture) > 1<<20 {
		return fmt.Errorf("expected generated PDF fixture no larger than 1 MiB")
	}
	// Exclusive creation refuses existing databases, symlinks and stale summaries.
	dbFile, err := os.OpenFile(*database, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("reserve new fixture database: %w", err)
	}
	if err := dbFile.Close(); err != nil {
		return err
	}
	out, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("reserve new summary: %w", err)
	}
	defer out.Close()
	// Fresh process only; preserve the actual clients' SSRF-safe transports.
	for name, value := range map[string]string{
		"SSRF_WHITELIST": "127.0.0.1", "SSRF_WHITELIST_EXTRA": "", "SSRF_DNS_WHITELIST_ONLY": "true",
		"WEKNORA_PADDLEOCR_VL_CLOUD_TIMEOUT": "30s", "HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "",
		"http_proxy": "", "https_proxy": "", "all_proxy": "",
	} {
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	reader := docparser.NewPaddleOCRVLCloudReader(map[string]string{
		"paddleocr_vl_cloud_base_url": *gateway + "/api/v2/ocr/jobs",
		"paddleocr_vl_cloud_token":    *token, "paddleocr_vl_cloud_model": "PaddleOCR-VL-1.6",
		"paddleocr_vl_cloud_use_seal_recognition": "true", "paddleocr_vl_cloud_use_chart_recognition": "false",
	})
	parsed, err := reader.Read(ctx, &types.ReadRequest{FileContent: fixture, FileName: "task10-fixture.pdf", FileType: "pdf"})
	if err != nil {
		return fmt.Errorf("actual PaddleOCR reader: %w", err)
	}
	if parsed == nil || parsed.Error != "" {
		return fmt.Errorf("actual PaddleOCR reader returned no result or an error: %+v", parsed)
	}
	if len(parsed.SourceBlocks) != 2 || len(parsed.ImageRefs) != 1 {
		return fmt.Errorf("expected two actual source blocks and one downloaded image; got %d/%d", len(parsed.SourceBlocks), len(parsed.ImageRefs))
	}
	s := summary{SchemaVersion: 1, ConsumerPin: consumerPin, BuildRevision: revision,
		Scope:          "pinned consumer clients and SQLite retriever; fixture chunk projection, not full app ingestion",
		DocumentSHA256: fmt.Sprintf("%x", sha256.Sum256(fixture)), Markdown: parsed.MarkdownContent,
		SourceBlocks: parsed.SourceBlocks, VectorWidth: vectorWidth}
	for _, img := range parsed.ImageRefs {
		if img.MimeType != "image/png" || len(img.ImageData) == 0 {
			return fmt.Errorf("fixture image missing downloaded PNG bytes/MIME")
		}
		if _, err := png.DecodeConfig(bytes.NewReader(img.ImageData)); err != nil {
			return fmt.Errorf("invalid downloaded fixture PNG: %w", err)
		}
		s.Images = append(s.Images, imageEvidence{Filename: img.Filename, OriginalRef: img.OriginalRef, MIME: img.MimeType, Bytes: len(img.ImageData), SHA256: fmt.Sprintf("%x", sha256.Sum256(img.ImageData))})
	}
	runes := []rune(parsed.MarkdownContent)
	locatorIndex := sourceloc.NewIndex(parsed.MarkdownContent, parsed.SourceBlocks)
	boxes := [][]float64{{0.1, 0.1, 0.9, 0.2}, {0.2, 0.3, 0.8, 0.4}}
	var texts []string
	for i, block := range parsed.SourceBlocks {
		if block.Start < 0 || block.End <= block.Start || block.End > len(runes) {
			return fmt.Errorf("source block %d has invalid rune range", i)
		}
		content := string(runes[block.Start:block.End])
		locators := locatorIndex.Locators(block.Start, block.End)
		if len(locators) != 1 || locators[0].Type != types.SourceLocatorPDF || locators[0].Mapping != "exact" || locators[0].Partial || locators[0].Page != i+1 || !reflect.DeepEqual(locators[0].BBox, boxes[i]) || locators[0].Quote != pageText[i] {
			return fmt.Errorf("source block %d does not map exactly to expected page, normalized box and bilingual quote: %+v", i, locators)
		}
		if !strings.Contains(content, pageText[i]) {
			return fmt.Errorf("source block %d lost the actual rune-sliced passage", i)
		}
		s.Chunks = append(s.Chunks, chunkEvidence{ChunkID: fmt.Sprintf("fixture-chunk-%d", i+1), Content: content, Start: block.Start, End: block.End, SourceLocators: locators})
		texts = append(texts, content)
	}
	embedder := openaiembeddings.New(openaiembeddings.Config{
		Endpoint: api.Endpoint{BaseURL: *gateway + "/compatible-mode/v1", Model: "text-embedding-v4", Auth: api.BearerAuth(*token)},
		Settings: api.EmbeddingsSettings{DimensionsField: "dimensions", SendEncodingFormat: true}, Dimensions: vectorWidth, Retry: api.RetryPolicy{},
	})
	vectors, err := embedder.Embed(ctx, texts, api.EmbedDocument)
	if err != nil {
		return fmt.Errorf("actual document embeddings: %w", err)
	}
	queryVectors, err := embedder.Embed(ctx, []string{query}, api.EmbedQuery)
	if err != nil {
		return fmt.Errorf("actual query embedding: %w", err)
	}
	if len(vectors) != len(texts) || len(queryVectors) != 1 {
		return fmt.Errorf("embedding result cardinality mismatch")
	}
	for _, vector := range append(append([][]float32(nil), vectors...), queryVectors...) {
		if len(vector) != vectorWidth {
			return fmt.Errorf("returned vector width %d, expected %d", len(vector), vectorWidth)
		}
		norm := 0.0
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("returned non-finite embedding")
			}
			norm += float64(value) * float64(value)
		}
		if norm == 0 {
			return fmt.Errorf("returned zero embedding")
		}
	}
	for _, vector := range vectors {
		s.DocumentWidths = append(s.DocumentWidths, len(vector))
	}
	s.QueryWidth = len(queryVectors[0])
	sqlitevec.Auto()
	db, err := gorm.Open(gormsqlite.Open(*database), &gorm.Config{})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	// Only this small citation projection is harness-owned. The actual consumer
	// repository owns all embedding metadata, vec0 and FTS5 tables below.
	if err := db.Exec(`CREATE TABLE chunks (id TEXT PRIMARY KEY, chunk_type TEXT NOT NULL, is_enabled INTEGER NOT NULL, content TEXT NOT NULL, start_at INTEGER NOT NULL, end_at INTEGER NOT NULL, source_locators TEXT NOT NULL)`).Error; err != nil {
		return err
	}
	var indices []*types.IndexInfo
	embeddings := make(map[string][]float32)
	for i, chunk := range s.Chunks {
		value, err := chunk.SourceLocators.Value()
		if err != nil {
			return err
		}
		if err := db.Exec(`INSERT INTO chunks (id, chunk_type, is_enabled, content, start_at, end_at, source_locators) VALUES (?, ?, ?, ?, ?, ?, ?)`, chunk.ChunkID, "text", 1, chunk.Content, chunk.Start, chunk.End, value).Error; err != nil {
			return err
		}
		indices = append(indices, &types.IndexInfo{Content: chunk.Content, SourceID: chunk.ChunkID, SourceType: types.ChunkSourceType, ChunkID: chunk.ChunkID, KnowledgeID: "fixture-doc", KnowledgeBaseID: "fixture-kb", IsEnabled: true})
		embeddings[chunk.ChunkID] = vectors[i]
	}
	repository := sqliteretriever.NewSQLiteRetrieveEngineRepository(db)
	if err := repository.BatchSave(ctx, indices, map[string]any{"embedding": embeddings}); err != nil {
		return fmt.Errorf("actual SQLite BatchSave: %w", err)
	}
	if err := sqlDB.QueryRow("SELECT sql FROM sqlite_master WHERE name = ?", "vec_embeddings_1024").Scan(&s.VectorTableSQL); err != nil {
		return err
	}
	if err := sqlDB.QueryRow("SELECT sql FROM sqlite_master WHERE name = ?", "lite_embeddings_fts").Scan(&s.FTSTableSQL); err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(s.VectorTableSQL), "using vec0") || !strings.Contains(s.VectorTableSQL, "float[1024]") || !strings.Contains(strings.ToLower(s.FTSTableSQL), "using fts5") {
		return fmt.Errorf("actual virtual vector/FTS tables were not initialized")
	}
	s.VectorResults, err = repository.Retrieve(ctx, types.RetrieveParams{Embedding: queryVectors[0], KnowledgeBaseIDs: []string{"fixture-kb"}, TopK: 2, Threshold: 0, RetrieverType: types.VectorRetrieverType})
	if err != nil {
		return fmt.Errorf("actual SQLite vector retrieval: %w", err)
	}
	// The actual repository joins tokenized terms with OR. English terms avoid
	// relying on CJK token boundaries while bilingual source ranges stay tested.
	s.KeywordResults, err = repository.Retrieve(ctx, types.RetrieveParams{Query: "valve maintenance", KnowledgeBaseIDs: []string{"fixture-kb"}, TopK: 2, RetrieverType: types.KeywordsRetrieverType})
	if err != nil {
		return fmt.Errorf("actual SQLite keyword retrieval: %w", err)
	}
	known := make(map[string]chunkEvidence)
	for _, chunk := range s.Chunks {
		known[chunk.ChunkID] = chunk
	}
	vectorIDs, keywordIDs := make(map[string]bool), make(map[string]bool)
	for i, sets := range [][]*types.RetrieveResult{s.VectorResults, s.KeywordResults} {
		expectedType := types.VectorRetrieverType
		seen := vectorIDs
		if i == 1 {
			expectedType, seen = types.KeywordsRetrieverType, keywordIDs
		}
		if len(sets) != 1 || sets[0] == nil || sets[0].Error != nil || sets[0].RetrieverEngineType != types.SQLiteRetrieverEngineType || sets[0].RetrieverType != expectedType || len(sets[0].Results) == 0 {
			return fmt.Errorf("missing, failed, or unexpected actual %s result set: %+v", expectedType, sets)
		}
		for _, hit := range sets[0].Results {
			if hit == nil {
				return fmt.Errorf("nil actual retrieval hit")
			}
			chunk, exists := known[hit.ChunkID]
			if !exists || hit.Content != chunk.Content || seen[hit.ChunkID] || math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) {
				return fmt.Errorf("actual hit identity/content/score mismatch")
			}
			seen[hit.ChunkID] = true
			if i == 0 {
				s.Candidates = append(s.Candidates, hit)
			}
		}
	}
	if len(vectorIDs) != 2 || len(keywordIDs) != 2 || s.Candidates[0].ChunkID != s.Chunks[1].ChunkID {
		return fmt.Errorf("expected both passages in actual vector/FTS hits with page 2 ranked first by vector")
	}
	var candidates []string
	for _, hit := range s.Candidates {
		candidates = append(candidates, hit.Content)
	}
	reranker := dashscoperank.New(dashscoperank.Config{
		Endpoint: api.Endpoint{BaseURL: *gateway, Model: "gte-rerank-v2", Auth: api.BearerAuth(*token)},
		Settings: api.RerankSettings{Path: "/api/v1/services/rerank/text-rerank/text-rerank", SendReturnDocs: false},
	})
	s.RerankResults, err = reranker.Rerank(ctx, query, candidates)
	if err != nil {
		return fmt.Errorf("actual native DashScope rerank: %w", err)
	}
	if len(s.RerankResults) != len(candidates) {
		return fmt.Errorf("actual rerank cardinality mismatch")
	}
	reranked := make(map[int]bool)
	for _, rank := range s.RerankResults {
		if rank.Index < 0 || rank.Index >= len(candidates) || reranked[rank.Index] || math.IsNaN(rank.Score) || math.IsInf(rank.Score, 0) || rank.Score < 0 || rank.Score > 1 {
			return fmt.Errorf("invalid actual rerank index or score")
		}
		reranked[rank.Index] = true
	}
	s.WinningIndex, s.WinningScore = s.RerankResults[0].Index, s.RerankResults[0].Score
	winningHit := s.Candidates[s.WinningIndex]
	var rawLocators string
	err = sqlDB.QueryRow(`SELECT id, content, start_at, end_at, source_locators FROM chunks WHERE id = ?`, winningHit.ChunkID).Scan(&s.Winner.ChunkID, &s.Winner.Content, &s.Winner.Start, &s.Winner.End, &rawLocators)
	if err != nil {
		return fmt.Errorf("load actual winning ChunkID citation: %w", err)
	}
	if err := s.Winner.SourceLocators.Scan(rawLocators); err != nil {
		return err
	}
	expected := known[winningHit.ChunkID]
	if !reflect.DeepEqual(s.Winner, expected) || !reflect.DeepEqual(s.Winner.SourceLocators, locatorIndex.Locators(s.Winner.Start, s.Winner.End)) {
		return fmt.Errorf("persisted winning citation does not equal the actual source-index mapping")
	}
	s.WinnerVectorHit, s.WinnerKeywordHit = vectorIDs[winningHit.ChunkID], keywordIDs[winningHit.ChunkID]
	if s.WinningIndex != 1 || !s.WinnerVectorHit || !s.WinnerKeywordHit || s.Winner.SourceLocators[0].Page != 1 || s.WinningScore != 0.95 {
		return fmt.Errorf("fixture rerank must switch the winner to the persisted page 1 citation with score 0.95 and both retrieval memberships")
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(s); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	fmt.Println("knowledge-consumer: actual pinned client/retriever fixture completed; summary written")
	return nil
}
