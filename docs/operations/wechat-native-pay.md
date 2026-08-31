# WeChat Pay Native operations

This runbook covers the first-party WeChat Pay APIv3 Native channel for CNY wallet top-ups. It does not cover JSAPI, H5, Mini Program, refunds, subscriptions, or Alipay.

## Safety boundary

- New orders are disabled unless `WECHATPAY_ENABLED=true`, payment compliance is confirmed, and every credential check passes.
- Keep credentials in the deployment secret store. Mount key files read-only; never paste key contents into database options, image layers, logs, or source control.
- Disabling the channel hides it and rejects new orders, while callbacks and reconciliation for existing orders continue as long as the credential files remain mounted.
- WeChat Pay does not provide a complete Native-payment sandbox. Final acceptance requires one explicitly approved small real CNY payment.

## Required merchant material

| Configuration | Source | Secret |
| --- | --- | --- |
| Merchant ID | WeChat Pay merchant platform | No |
| AppID | App/official-account platform bound to the merchant | No |
| Merchant API certificate serial | Merchant API certificate page | No |
| Merchant API private key PEM | Generated with the merchant API certificate request | Yes |
| APIv3 key | Merchant platform API security page; exactly 32 bytes | Yes |
| WeChat Pay public key ID | Merchant platform API security page | No |
| WeChat Pay public key PEM | Downloaded for the public key ID | No |

The merchant API certificate serial is not the WeChat Pay public key ID. Do not substitute one for the other.

## Environment and files

Set the administrator `ServerAddress` option to the exact public HTTPS origin before enabling the channel. For production, this produces the callback URL:

```text
https://llmrouter.quukk.com/api/wechat-pay/notify
```

Example deployment variables:

```bash
WECHATPAY_ENABLED=false
WECHATPAY_MCH_ID=<merchant-id>
WECHATPAY_APP_ID=<bound-app-id>
WECHATPAY_MCH_CERT_SERIAL=<merchant-api-certificate-serial>
WECHATPAY_MCH_PRIVATE_KEY_PATH=/run/secrets/wechatpay/apiclient_key.pem
WECHATPAY_API_V3_KEY_PATH=/run/secrets/wechatpay/apiv3-key
WECHATPAY_PUBLIC_KEY_ID=<wechat-pay-public-key-id>
WECHATPAY_PUBLIC_KEY_PATH=/run/secrets/wechatpay/wechatpay_public_key.pem
WECHATPAY_MIN_TOPUP_CNY=1.00
WECHATPAY_MAX_TOPUP_CNY=5000.00
WECHATPAY_DESCRIPTION=LLMRouter wallet top-up
```

Restrict the secret files before mounting them:

```bash
chmod 600 apiclient_key.pem apiv3-key
```

The APIv3-key file must contain the 32-byte key itself. The merchant private key must be a parseable PEM file. Group or other permissions on either secret file cause readiness validation to fail.

## Public-key rotation

During a planned WeChat Pay public-key rotation, add the new key as an overlap verifier without replacing the current key immediately:

```bash
WECHATPAY_NEXT_PUBLIC_KEY_ID=<new-wechat-pay-public-key-id>
WECHATPAY_NEXT_PUBLIC_KEY_PATH=/run/secrets/wechatpay/wechatpay_public_key_next.pem
```

Both next-key variables must be supplied together. Restart the application, verify API responses and callbacks signed by the new key, then promote the new ID/path to the current variables and remove the next-key variables in a later controlled restart. The current key remains the encryption key during the overlap.

Do not rotate merchant ID, AppID, or public origin in place. Disable new orders and drain or close pending orders first.

## Staged enablement

1. Back up the database and deploy the schema/code with `WECHATPAY_ENABLED=false`.
2. Mount all credential files and set the public `ServerAddress`.
3. Confirm payment compliance in the administrator payment settings.
4. Use a separate staging/canary instance or a controlled maintenance window for the first payment. The enable flag is global, not user-specific.
5. Set `WECHATPAY_ENABLED=true` and restart the canary.
6. Sign in and call `GET /api/user/topup/info`. Continue only when `data.enable_wechat_pay` is `true` and the displayed minimum/maximum are correct.
7. Create one small order from the CNY card. Verify the UI shows the exact same yuan amount and a WeChat Native QR code.
8. After explicit approval, pay it and verify all of the following: callback HTTP success, local `pending -> success`, exact `paid_amount_minor`, one quota credit, updated wallet balance, and one order-history record.
9. Re-query/replay the completed order and confirm the balance does not increase again.
10. Leave one order unpaid and verify reconciliation closes it before it becomes `expired` locally.
11. Recheck Stripe, Waffo, and redemption flows before broad enablement.

## Operational checks

The order-status endpoint reads only local state and is safe for browser polling:

```text
GET /api/user/wechat-pay/orders/:trade_no
```

Watch application logs for these event families without exposing credential contents:

- `WeChat Pay order created`
- `WeChat Pay callback settled`
- `WeChat Pay reconciliation`
- `WeChat Pay callback incident recorded`
- configuration/key parsing failures

A successful checkout screen alone is not acceptance. The verified callback or reconciliation must settle the exact local order and the user-visible balance must change exactly once.

## Stop and rollback

Immediately set `WECHATPAY_ENABLED=false` and restart if callback verification fails, merchant/AppID/amount identity mismatches occur, settlement reports a provider transaction conflict, or wallet credit differs from the order snapshot. Keep credentials mounted so already-created orders can still be verified and reconciled.

Application rollback must not reverse successful wallet credits. Preserve the payment/order tables and incident logs, then restore the prior immutable image only after new orders are disabled.
