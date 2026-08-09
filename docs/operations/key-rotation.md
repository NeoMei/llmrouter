# Credential and key rotation

Use the secret manager's version history and two-person approval. Capture only key identifiers in incident/release evidence—never secret values.

## Provider and object-store credentials

1. Create the replacement credential with the minimum required scope.
2. Validate it on one disabled/canary channel or isolated bucket operation.
3. Update all gateway instances and roll them one at a time.
4. Verify readiness and an authenticated operation on every node.
5. Revoke the old credential and confirm it can no longer authenticate.

## `SESSION_SECRET`

The application does not support simultaneous old/new session secrets. Rotation invalidates sessions, refresh cookies, security proofs, and temporary auth flows.

1. Announce the login interruption and stop administrative changes.
2. Replace `SESSION_SECRET` atomically on every node and restart all nodes in one maintenance window.
3. Clear shared auth/session cache if directed by the incident commander.
4. Verify old sessions fail and fresh login, refresh, logout, and cross-node requests succeed.

## `GATEWAY_AES_KEY`

Do **not** replace this environment variable in place. Stored provider credentials are encrypted with the current key and there is no dual-key decrypt or online re-encryption path. An unplanned replacement makes existing ciphertext unreadable.

The supported operational choice is to export/re-enter every credential through an approved migration utility while the old key is still available, verify decryption with the new key in an isolated copy, then perform a maintenance-window cutover. Until a dedicated re-encryption command is implemented and certified, loss of the old key requires restoring the secret or database backup; it cannot be repaired from ciphertext.

## Emergency revocation

Disable affected channels/API keys first, revoke at the upstream provider, rotate scoped credentials, audit access and usage logs, then restore service. Follow `incident-response.md`; do not wait for a full application deployment to revoke a compromised credential.
