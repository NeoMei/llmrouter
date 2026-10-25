# Actual pinned knowledge consumer fixture

This opt-in executable calls the consumer's actual PaddleOCR reader, source
locator index, OpenAI embedding client, SQLite vec0/FTS5 repository and native
DashScope reranker. It is intentionally under `testdata`: ordinary LLMRouter
tests must not build/download a second module. It is **not** desktop startup,
full application ingestion, supplier OCR accuracy, or live paid-provider
acceptance. A passing compile alone is not integration acceptance.

## Build and isolation

Use an isolated checkout of `NeoMei/embedded-knowledge` at exactly
`f5adb9eac166e7d3da7dceed7aa50049d4c4db1f`. Its module is
`github.com/Tencent/WeKnora`; its `internal` imports require building this source
inside that module. Verify HEAD and the absence of tracked changes there before
copying only `main.go` into the new disposable `cmd/task10consumer/` directory.
Do not overwrite an existing command directory. Do not run SuperWagie's prepare
script or touch its installation, backend supervisor, user index or credentials.
Do not change either module's dependencies.

From that consumer checkout, build with an absolute temporary output path:

```sh
GOTOOLCHAIN=go1.26.10 CGO_ENABLED=1 go build -buildvcs=true -tags sqlite_fts5 -o "$TASK10_BINARY" ./cmd/task10consumer
```

The binary verifies its VCS revision against the pin before accessing fixtures
or making requests. Copying the untracked harness makes `vcs.modified=true`;
the parent must separately verify that tracked consumer files remain unchanged.
Do not suppress VCS stamping. Capture command, working directory, environment
overrides, stdout, stderr and exit status. Existing sqlite-vec macOS header
deprecation warnings may appear on cold builds; preserve their actual output.

Run in a **fresh child process** against the parent's actual production-router
fixture, with a parent-enforced deadline (for example 180 seconds):

```sh
"$TASK10_BINARY" \
  -gateway-url "$TASK10_GATEWAY_ORIGIN" \
  -platform-token "$TASK10_FIXTURE_PLATFORM_TOKEN" \
  -database "$TASK10_TEMP_DIR/consumer.sqlite" \
  -summary "$TASK10_TEMP_DIR/summary.json" \
  -documentpath "$TASK10_TEMP_DIR/task10-fixture.pdf"
```

The URL must be exactly `http://127.0.0.1:<port>` with no path/query/userinfo,
port in 1024–65535, excluding SuperWagie's port 17808. All tokens must be ephemeral
fixture tokens. The database must be a new absolute `.sqlite` path in a dedicated
subdirectory of the OS temporary directory. Database and summary use exclusive
creation: existing files/symlinks are refused. Failed runs may leave an empty
summary and partial fixture DB; only exit 0 plus valid complete JSON is success.
Use a new temporary directory for each attempt. The fixture PDF must start with
`%PDF-` and be at most 1 MiB; the parent owns actual two-page PDF generation.

The child sets `SSRF_WHITELIST=127.0.0.1`, clears `SSRF_WHITELIST_EXTRA`, enables
`SSRF_DNS_WHITELIST_ONLY=true`, and replaces the process runtime whitelist with
that exact host. It clears HTTP/HTTPS/ALL proxy variables in both cases and
preserves the actual SSRF-safe clients. This whitelist is host-scoped, not
port-scoped: the parent must return only its own fixture grant URLs. OCR polling
timeout is 30 seconds and model calls have a 90-second context, but the reader's
JSONL/image downloads use their own internal timeouts, hence the parent deadline.
stdout/stderr contain real consumer logs; **read JSON only from `-summary`**.

## Exact parent fixture contract

Bind/authorize these exact public names and paths using fixture accounts:

| Stage | Model and wire contract |
| --- | --- |
| OCR | `PaddleOCR-VL-1.6`, multipart `POST /api/v2/ocr/jobs`, then real owned job polling and granted JSONL/image downloads |
| Embeddings | `text-embedding-v4`, `POST /compatible-mode/v1/embeddings`, `dimensions:1024`, `encoding_format:"float"`, same identity/space for documents and query |
| Rerank | `gte-rerank-v2`, native `POST /api/v1/services/rerank/text-rerank/text-rerank`, `parameters.top_n:2`, `return_documents:false` |

The OCR reader explicitly enables seal recognition and disables chart
recognition; its real recognition map contains additional defaults. Parent
assertions should inspect the actual multipart map. Return a completed job on
the first GET. The gateway must serve JSONL/images as absolute grant URLs on
`127.0.0.1` without requiring a bearer header for the downloads.

The deterministic two-page PDF and returned OCR layout must agree. Page 1 has
this exact text followed by `\n\n![fixture](images/diagram.png)`:

```text
冷却规范：阀门必须关闭。 Cooling manual specifies valve closure.
```

Page 2 has exactly:

```text
检修规范：阀门检查周期为七天。 Maintenance interval is seven days.
```

Return one JSONL record with `result.layoutParsingResults` containing the two
pages. Each page has `markdown.text`, `markdown.images`, and `prunedResult` with
`width:600`, `height:800`, and `parsing_res_list`. Each list has one text block
whose `block_content` is that page's exact sentence. Page 1 `block_bbox` is
`[60,80,540,160]`; page 2 is `[120,240,480,320]`. Page 1's image map is
`{"images/diagram.png":"<fixture PNG URL>"}`; page 2's map is empty. The image
must be an actual valid PNG. The parent asserts its expected SHA-256 against the
child's downloaded bytes, and separately compares the document SHA-256.

The child builds one fixture chunk for each **actual** source block by slicing
the actual Markdown in runes. Aligner ranges may include trailing whitespace
or the image markup, so embedding fixtures should identify input by the exact
sentence it contains, not assume the chunk equals just that sentence. Return
1024 finite nonzero floats: `e0` for the page 1 input, `e1` for page 2, and
`[0.6,0.8,0,...]` for query `阀门维护规范`. Return valid usage and original
embedding response indices. Configure one account failure (429) and one healthy
sibling with identical model/version/dimensions/text semantics in the parent.
Consumer embedding retry policy is zero retries, so rotation belongs to the
gateway; parent assertions must prove actual sends and billing.

The actual vector query must produce both passages, page 2 first. Actual FTS5
query is `valve maintenance`; the pinned repository OR-combines terms and must
return both passages. Rerank documents are the unmodified actual vector hit
slice, in that order. Return native results `[{"index":1,"relevance_score":0.95},
{"index":0,"relevance_score":0.2}]` with valid usage. Do not echo documents.
The child preserves these original indices and loads the winning ChunkID's
persisted source range and locators from SQLite; it does not infer a citation
from a candidate position or fabricated page label.

## Persistence and summary schema

`sqlitevec.Auto()` registers the actual extension. The harness creates only a
small fixture `chunks` projection (`id`, `chunk_type`, `is_enabled`, `content`,
`start_at`, `end_at`, `source_locators`). Locators are assigned by actual
`sourceloc.NewIndex(...).Locators(...)` and persisted/reloaded using the actual
`SourceLocators.Value/Scan`. This is expressly **not** the full app's chunk
migration or ingestion service. The consumer repository owns `lite_embeddings`,
`vec_embeddings_1024 USING vec0` and `lite_embeddings_fts USING fts5`; the harness
does not substitute those tables or retrieval implementations.

JSON schema version 1 exposes:

- `consumer_pin`, verified `build_revision`, `scope`, `document_sha256`.
- `markdown`, `source_blocks` (actual consumer `Start`, `End`, `Locator` fields),
  `images` (`filename`, `original_ref`, `mime`, `bytes`, `sha256`).
- `chunks`: fixture IDs, exact `content`, rune `start/end`, actual
  `source_locators` with PDF page/normalized box/exact mapping/quote.
- `vector_width`, `document_vector_widths`, `query_vector_width`,
  `vector_table_sql`, `fts_table_sql`.
- `vector_results`, `keyword_results`, `candidates`: actual consumer exported
  structures, preserving Go field names such as `Results`, `ChunkID`, `Content`,
  `Score`, `RetrieverType`, `RetrieverEngineType`; successful `Error` is null.
- `rerank_results`: actual `Index`, `Score`, `Text`; `winning_rerank_index`,
  `winning_rerank_score`; `winner`: reloaded chunk content/range/locators;
  `winner_vector_hit`, `winner_keyword_hit`.

Exit 0 requires both actual nonempty retrieval result sets, both passage IDs,
1024-wide finite nonzero document/query vectors, two exact bilingual PDF
mappings/normalized boxes, a valid downloaded PNG, the expected vector ordering,
and rerank switching to persisted page 1 with score 0.95. The parent still owns
assertions on hashes, exact requests and upstream identity, failed-account
rotation, token/wallet/log effects, owned/frozen OCR jobs, terminal CAS and one
OCR settlement. Preserve those assertions separately from this executable's
consumer checks. No full gateway chain has passed merely because this source
builds or the binary rejects unsafe inputs.
