# UniHub-owned LLMRouter acceptance — 2026-10-27

Public LLMRouter identity and billing now belong to UniHub. Native users, wallets,
API tokens and subscriptions remain behind the separate loopback operator
listener. Public native login/token/top-up/relay requests return 403. The private
channel API still requires operator authentication (401 without credentials).
Signed callbacks remain available for existing orders.

## Deployment and rollback

- UniHub source `587d1bf`, release `/opt/unihub/releases/20261027-unihub-owned-587d1bf`.
- LLMRouter source `f6dbbf0a1`, image `sha256:42b427cab2748898561d1505101d06fbcfb7b932fbd3d1bb9d5fd152953635b3`.
- SuperWagie source `21ac9ce43`; prior `f9dd62ea8` supplied 22-model defaults.
- Remote evidence: `/opt/llmrouter/operations/unihub-owned-20261027/`.
- Cutover backup `backup-20261027T120118`; previous UniHub `/opt/unihub/releases/v0.4.6`, previous container `llmrouter-before-unihub-owned-120122`.
- Final adapter upgrade backup `upgrade-121619`; previous container `llmrouter-before-upgrade-121636`.
- Database/config backups preceded later mutations. Container hostname mapping for
  the private UniHub service is explicit and survives recreation.

## Actual acceptance

All 22 published chat models returned HTTP 200, settled in CNY/6 and rejected
logical-call replay with HTTP 409. Ordinary and streaming modes alternated across
the catalog. All 22 had exactly one stored provider attempt.

SuperWagie's actual account/model runtimes logged in using its headless client,
selected the account, obtained a product-bound grant, sent streaming
`DeepSeek-V4-Flash-A` through the managed local proxy/public gateway, received
`[DONE]` and reconciled a SETTLED receipt charging 14 CNY micros. A packaged desktop
click-through was not run.

Independent exact-rational recomputation matched all 23 call charges: **28498 CNY
micros (CNY 0.028498)**. After sandbox recharge of 100000000 CNY micros, the wallet
held **99971502**, exactly recharge minus consumption. The native routing
principal's quota, used quota and request count remained zero.

Both flat-price models settled at **73 CNY micros/request**. Other prices use the
versioned token-rate manifest: native operator price conversion uses USD2RMB 7.3
and ratio factor 14.6 CNY micros/token. This is not supplier invoice reconciliation.

An early MiniMax sandbox test retained an unconfirmed hold before its usage-alias
fix. A one-time operator waiver for that isolated acceptance account was backed up
under `sandbox-waiver-121959`; gateway and UniHub now report RELEASED. Supplier
cost is absorbed by the operator. No production-user call was replayed or waived.

## Checks and limits

UniHub full Go race tests passed with isolated PostgreSQL/Formance. Actual TCP
gateway tests covered CREDIT/CNY, lost settlement response, restart recovery,
duplicate rejection and unchanged native quota. LLMRouter focused/race tests,
frontend type/build and 23 UniHub frontend tests passed. SuperWagie model/account
suites and type-check passed. Independent reviews found no remaining confirmed
blocker. Zhipu streaming was specifically verified with final SETTLED receipts.

Both health endpoints and public static assets return 200. Rate consistency has
22 models, no missing or duplicate rates. Sandbox-only extra model/rate records
remain separate.

Supplier calls were real and charged upstream as authorized. UniHub recharge and
membership funding were **sandbox**; real merchant collection/activation was not
tested. OCR, embedding, rerank, speech, image and video are outside this chat grant.
