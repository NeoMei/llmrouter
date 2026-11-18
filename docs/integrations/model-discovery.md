# Model discovery and specialized endpoints

`GET /v1/models` lists the model IDs visible to the supplied key. The list can
include chat, OCR, embedding, image, audio, and video models. Inclusion alone
is not a health check and does not mean every model accepts chat requests.

When configured and visible for the key, entries can contain:

- `context_length` and `max_output_tokens`: declared gateway limits. Omitted
  values are unknown, not zero. A managed key may have lower effective limits.
- `operations`, `endpoints`, `input_modalities`, and `output_modalities`:
  declared operations, gateway paths, and media types. For managed keys,
  `operations` and `endpoints` only include operations executable under that
  key's verified authorization and policy at discovery time.
- `capabilities.tools` and `capabilities.vision`: configured information when
  known. An omitted value is unknown, not false.
- `alias_of`: the public route for a configured alias, when that route is
  visible to the key.
- `configured_upstream_models`: model IDs configured on authorized targets.
  This list describes possible configured targets, not which one processed a
  particular request. Subsequent channel mappings or upstream behavior can
  change the model actually used; multiple IDs do not promise ordering or
  availability. Check the generation response's `model` where provided.

Older ability-only entries may lack metadata. Prices are omitted when no
verified price contract is available; do not infer prices from these fields.

## PaddleOCR jobs

`PaddleOCR-VL-1.6` declares OCR jobs, not chat completions. For the PaddleOCR
compatible job format, submit one image or PDF as multipart data:

```bash
curl --fail-with-body https://llmrouter.quukk.com/api/v2/ocr/jobs \
  -H "Authorization: Bearer $LLMROUTER_API_KEY" \
  -F 'model=PaddleOCR-VL-1.6' \
  -F 'file=@document.png'
```

On successful creation the response includes `data.jobId`. Poll the job with
the same key:

```bash
curl --fail-with-body "https://llmrouter.quukk.com/api/v2/ocr/jobs/$JOB_ID" \
  -H "Authorization: Bearer $LLMROUTER_API_KEY"
```

When `data.state` is `done`, the response can include a gateway result URL in
`data.resultUrl.jsonUrl`. The gateway also exposes `/v1/ocr` and
`/v1/ocr/{id}` as its canonical OCR interface. Its response shape differs from
the PaddleOCR-compatible interface. Job submission may incur charges, and a
failed or uncertain response is not proof that no job was created. Endpoint
metadata is a protocol description, not a guarantee that the upstream service
is currently healthy.

## Multimodal embeddings

Models declared for `multimodal.embeddings.create` use
`POST /v1/multimodal/embeddings`, not the text-only `/v1/embeddings` contract.
For example, a compatible model accepts `{"model":"MODEL_ID","input":[{"text":"test"}]}`.
A valid route must also declare a fixed embedding space and satisfy the key's
billing and capability policies. A listed model or supported protocol by itself
does not prove that such a route is configured or operational.

## Configuring reliable limits

Populate `context_window`, `max_output_tokens`, and `supports_tools` on a
public route only after verifying the model actually selected for every enabled
target and its channel mapping. An upstream model's advertised limits are not
automatically the gateway's effective limits: account plan, wire protocol,
router admission policy, and fallback targets can impose lower limits. For a
multi-target route, publish a conservative value valid for every eligible
execution path, or leave it unknown. Recheck the declaration when a target,
model mapping, account plan, or provider limit changes. A single successful
short request does not establish a context limit or reliable tool support.
For managed keys, their verified operation authorization and effective
admission policies also constrain the values presented by discovery.

The ordinary `GET /v1/models/{id}` implementation currently uses a separate
static model table and may differ from the authorized list; use the list for
these fields until the individual lookup contract is reconciled.
