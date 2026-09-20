# PAIMOS — Hardening Guide & Reference Architecture

**Owner:** the maintainer (single-person operation as of v2.0).
**Companion docs:** [`THREAT_MODEL.md`](THREAT_MODEL.md) (what must be true), [`CONFIGURATION.md`](CONFIGURATION.md) (every env var), [`DEPLOY.md`](DEPLOY.md) (release + rollback runbook), [`INCIDENT_RESPONSE.md`](INCIDENT_RESPONSE.md) (when something goes wrong), [`CONTINUITY.md`](CONTINUITY.md) (when the maintainer is out).
**Audience:** operators bringing a PAIMOS deployment online. Pre-launch and recurring (every six months, or after material infrastructure change).

---

## 0 · Purpose & scope

This document is the **operator-facing companion** to [`THREAT_MODEL.md`](THREAT_MODEL.md). Where the threat model says *what must remain true*, this guide says *how to make it true* in a real deployment — and *how to verify it*.

Three uses:

1. **Pre-launch hardening pass.** Walk every checklist item in §3 before exposing a fresh PAIMOS instance to users.
2. **Recurring review.** Re-walk the checklist every six months, or after any material infrastructure change (new reverse proxy, new TLS provider, new MinIO bucket, new IdP).
3. **Audit evidence.** A reviewer asks "is this deployment hardened?" — the answer is "here's the checklist; here are the verification commands; here are the outputs."

This guide is **not**:

- A vendor-specific runbook. The reference architecture is reverse-proxy-agnostic; examples use Caddy because that's what `paimos.com` itself uses, but the patterns translate cleanly to nginx / Traefik / Apache.
- A substitute for [`SECURITY.md`](../SECURITY.md). That's the inbound disclosure policy; this is the outbound deployment posture.
- An exhaustive list of every defensive measure imaginable. The bar is *defensive measures relevant to PAIMOS's threat model* — see [`THREAT_MODEL.md` §5](THREAT_MODEL.md) for what's deliberately out of scope.

---

## 1 · Reference architecture (production)

The recommended PAIMOS production deployment shape:

```
                ┌──────────────────────┐
                │   Browser / Client   │
                └──────────┬───────────┘
                           │  HTTPS  (TLS 1.2+, HSTS, valid cert)
                           ▼
            ┌─────────────────────────────┐
            │  Reverse proxy              │  ← terminates TLS
            │  (Caddy · Traefik · nginx)  │  ← rate-limit edge (best-effort)
            │                             │  ← request body limits
            └──────────────┬──────────────┘
                           │  HTTP   (loopback or private network)
                           ▼
            ┌─────────────────────────────┐
            │  PAIMOS Go binary  :8888    │  ← single process; set runtime user
            │  COOKIE_SECURE=true         │  ← chi router + auth middleware
            │  PAIMOS_AUDIT_SESSIONS=true │  ← session-mutation audit on
            └──┬─────────┬────────┬───────┘
               │         │        │
               ▼         ▼        ▼
         ┌─────────┐  ┌──────┐  ┌────────────────────┐
         │ SQLite  │  │MinIO │  │ optional providers  │
         │ + WAL   │  │ S3   │  │ SMTP · OIDC · LLM   │
         │ encr-FS │  │      │  │                    │
         └─────────┘  └──────┘  └────────────────────┘
                                          │
                                          ▼
                                ┌──────────────────┐
                                │ secret manager   │  ← env vars
                                │ (Vault, etc.)    │     sourced from here
                                └──────────────────┘

         ┌──────────────────┐         ┌──────────────────┐
         │ stdout audit →   │         │ off-site backup  │
         │ log aggregator   │         │ (object store /  │
         │ (Loki, Splunk…)  │         │  separate host)  │
         └──────────────────┘         └──────────────────┘
```

**The five non-negotiables:**

1. **TLS at the edge.** PAIMOS does not terminate TLS itself. A reverse proxy must do that. PAIMOS sets `Secure` on cookies when `COOKIE_SECURE=true`, which is meaningful only behind real HTTPS.
2. **Single-port exposure.** PAIMOS exposes one port (`:8888` by default). The reverse proxy is the only thing that should reach it.
3. **`$DATA_DIR` on isolated storage.** Multi-tenant hosts must give PAIMOS its own filesystem space. Encryption-at-rest is the operator's call (see §3.6).
4. **Outbound provider creds in env vars, not on disk.** OIDC / SMTP / MinIO credentials live in env. The OpenRouter API key is the one exception (stored in SQLite by design — admin-set via UI), and that's why the AI assist surface explicitly recommends encrypted FS.
5. **Audit egress.** Stdout audit lines (`audit: ai_action ...`, `audit: session_mutation ...`) are useful only if they're collected. A SIEM / log aggregator is the egress.

**What's optional:**

- **MinIO/S3** — only needed if attachments are used. PAIMOS hides drop zones when unconfigured.
- **SMTP** — only needed for password reset (and only outbound; PAIMOS doesn't receive mail).
- **OIDC** — only needed for SSO by default. Instances using
  `AUTH_PASSWORD_LOGIN=disabled` require OIDC for new web logins and give up
  the local-admin browser fallback, including `?method=password` and password
  recovery. Verify a keyring-held CLI API key before cutover: it remains the
  break-glass path during an IdP outage. See [password-login policy](CONFIGURATION.md#instance-password-login-policy-pai-1044).
- **OpenRouter** — only needed for AI assist; default-disabled until an admin enables it.

---

## 2 · Pre-deployment checklist

Before you `docker compose up` for the first time:

- [ ] **HTTPS certificate ready.** Let's Encrypt via Caddy is the simplest path; internal CA works equally well.
- [ ] **Domain DNS** pointed at the host. The cert renewal flow needs DNS to resolve.
- [ ] **`$DATA_DIR` parent directory** exists on the chosen storage volume.
- [ ] **Encryption-at-rest** decision made: required (per your threat model) or accepted as out-of-scope. See §3.6.
- [ ] **Backup destination** ready: a separate disk, object store, or off-site location. The deploy script (`scripts/deploy.sh`) creates a tarball before every deploy; you need somewhere to keep it.
- [ ] **Reverse proxy configured** with the response-header forwarding documented in §3.1.
- [ ] **Operator user account** on the host has Docker access (`docker compose` permission).
- [ ] **Secret manager** chosen: 1Password / Vault / Docker secrets / systemd credentials / etc. — mounted as narrow, read-only runtime files. Not the maintainer's clipboard.
- [ ] **Initial `ADMIN_PASSWORD_FILE`** contains a CSPRNG-generated password and is ready for first boot. **Will be rotated immediately after first login and the file binding removed** — see §3.2.
- [ ] **Branding decision:** product name, company, email-from per `BRAND_*` env vars in [`CONFIGURATION.md`](CONFIGURATION.md). Default is `PAIMOS`; rebrand on first boot if you intend to.

---

## 3 · Hardening checklist

The seven domains map roughly to [`THREAT_MODEL.md` §4](THREAT_MODEL.md) invariant groups. Run through every item; record any *deliberate* deviations in your operations log.

### 3.1 · TLS, network, and edge

| Item | Verification | Threat-model invariant |
|---|---|---|
| `COOKIE_SECURE=true` set in env | `docker compose exec paimos env \| grep COOKIE_SECURE` | INV-AUTH-02 |
| Reverse proxy enforces HTTPS; HTTP requests redirect 301 → HTTPS | `curl -I http://your.host/api/health` returns `301` to `https://` | – |
| HSTS header is set on responses | `curl -sI https://your.host/api/health \| grep -i strict-transport-security` | – |
| Security headers present (`X-Frame-Options=SAMEORIGIN`, `nosniff`, `Referrer-Policy`, `Permissions-Policy`) | `curl -sI https://your.host/api/health` | INV-NETWORK §2.1 (PAI-114) |
| CSP-Report-Only sink is configured (or knowingly ignored) | `curl -sI https://your.host/api/health \| grep -i csp` shows the policy; report endpoint is reachable from your CSP `report-uri` if you provided one | – |
| PAIMOS backend has explicit HTTP server timeouts (`ReadHeaderTimeout=5s`, `ReadTimeout=30s`, `WriteTimeout=2m`, `IdleTimeout=2m`) | startup uses the built-in `http.Server` timeout configuration; keep reverse-proxy timeouts compatible with these bounds | – |
| Reverse proxy does not bypass PAIMOS's rate limits (i.e., login / forgot / reset / TOTP-verify still rate-limit at PAIMOS layer) | trigger 6 failed logins from a clean IP; confirm the 6th is rate-limited | INV-AUTH-04 |
| Request body size limit at the reverse proxy (recommended: 32 MiB; PAIMOS attachment cap defaults to 25 MiB) | upload a 100 MiB file via the attachment endpoint; expect a `413` from the proxy before it reaches PAIMOS | – |
| TLS protocol minimum is TLS 1.2 (TLS 1.3 preferred); SSL 3 / TLS 1.0 / 1.1 disabled | `nmap --script ssl-enum-ciphers -p 443 your.host` or `https://www.ssllabs.com/ssltest/` | – |

### 3.2 · Authentication

| Item | Verification | Invariant |
|---|---|---|
| Initial admin password rotated within 5 minutes of first login | UI: `Settings → Account → Change password` after login; record the rotation in your operations log | INV-AUTH-01 |
| `ADMIN_PASSWORD` / `ADMIN_PASSWORD_FILE` **removed** after the first boot completes | `docker compose exec paimos sh -c 'test -z "${ADMIN_PASSWORD+x}" && test -z "${ADMIN_PASSWORD_FILE+x}"'` succeeds without printing either value | INV-AUTH-01 |
| All admin users have TOTP enabled | UI: `Settings → Users` shows TOTP-enabled flag per admin row | INV-AUTH-05 |
| OIDC is configured **only if** required by the deployment; otherwise disabled (no `OIDC_ISSUER_URL`) | Login page shows or hides "SSO" button accordingly | INV-AUTH-08 |
| OIDC IdP emits `email_verified=true`; PAIMOS is invite-only by default for unknown emails | unverified users redirect to `/login?sso_error=email_required`; unknown users redirect to `/login?sso_error=invite_required` unless `OIDC_PROVISION_MODE=auto-create` is explicitly set | INV-AUTH-08 |
| Password reset link expiry is the documented 60 min; never extended | `PAIMOS_RETENTION_DAYS_RESET_TOKENS=7` (default; this is post-use audit retention, not link-expiry) | INV-AUTH-06 |
| Rate-limit window observed for login / forgot / reset / TOTP-verify (5 attempts / 10 min / IP+identity) | trigger 6 attempts with bad credentials; expect 429 on the 6th | INV-AUTH-04 |
| **Dev-login route is absent from this binary.** Production builds do not compile the `dev_login` package; CI's `PAI-270` step asserts the canary strings are absent on every release. See [`DEV_LOGIN.md`](DEV_LOGIN.md). | `strings $(which paimos) \| grep -c 'DEV-LOGIN ROUTE ENABLED'` returns `0` | INV-AUTH-09 (PAI-267) |

### 3.3 · Authorization

| Item | Verification | Invariant |
|---|---|---|
| Project access matrix reviewed (`Settings → Permissions` or `GET /api/permissions/matrix`) | every user has the lowest level that lets them do their job | INV-AUTHZ-01 / 02 / 03 |
| No "everyone is admin" anti-pattern | `paimos issue list --assignee admin` returns expected count, not all users | – |
| External-role users (if any) only have portal access; cannot reach internal routes | manual: log in as an external user; navigate to `/projects/1`; expect redirect to `/portal` | INV-AUTHZ-06 |
| New project default access is `editor` for member users (PAIMOS default) — verified to match your operational expectation | first-create-project audit | INV-AUTHZ-05 |
| API keys created by users have scope == that user's role; admin-issued API keys honour the *creator's* role, not the issuer's | UI: `Settings → Account → API keys` (per user); admin can list keys via user-detail page | INV-AUTH-03 |

### 3.4 · Files and uploads

| Item | Verification | Invariant |
|---|---|---|
| Application upload/serve hardening rejects active content and forces non-inline-safe stored bytes to download; reverse proxy may keep a belt-and-suspenders `Content-Disposition: attachment` rule for user-uploadable active types | upload HTML/SVG/JS payloads; expect upload rejection or forced download with restrictive CSP on legacy bytes | **INV-FILES-03 (PAI-110 shipped; freshness tracked by PAI-551)** |
| MinIO bucket access scoped to PAIMOS only (separate IAM credential, not the root credential) | MinIO admin UI: confirm the access key has `s3:GetObject` / `PutObject` only for `${BUCKET}/*` | – |
| Attachment uploads bounded by `BRAND_MINIO_BUCKET` env (default `paimos-attachments`); not a shared bucket with other apps | `MINIO_BUCKET` is dedicated | INV-FILES-01 |
| MinIO + PAIMOS use TLS to each other in production (or share a private network) | `MINIO_USE_SSL=true` if endpoint is on the public internet | – |
| Branding-asset uploads (logo, favicon) restricted to admin role | UI: non-admin user cannot reach `Settings → Visual → Workspace Branding` | INV-FILES-06 |
| Image upload size limit (PAIMOS default ~25 MiB; configurable) sane for your operational context | upload a 30 MiB image; expect `413` | – |

> **Note:** PAI-110 shipped application-layer active-content upload hardening. Keeping the proxy-layer defence is still cheap defense in depth, but it is no longer the primary mitigation. See `THREAT_MODEL.md` INV-FILES-03; freshness tracking lives in PAI-551.

### 3.5 · Audit and observability

| Item | Verification | Invariant |
|---|---|---|
| `PAIMOS_AUDIT_SESSIONS=true` (default in v2.0) | `docker compose exec paimos env \| grep PAIMOS_AUDIT_SESSIONS` | INV-AUDIT-03 |
| Stdout audit lines forwarded to a log aggregator | `docker compose logs paimos \| grep "^audit:"` shows lines; aggregator query confirms ingest | INV-AUDIT-01 / 02 |
| Retention windows reviewed and tuned (`PAIMOS_RETENTION_DAYS_*`) | current values match your compliance posture; defaults documented in [`CONFIGURATION.md`](CONFIGURATION.md) | INV-AUDIT-06 |
| Incident-log table seeded for the first incident; not a surprise table on day one | UI: `Settings → Incidents` (admin-only); empty list with "no incidents" empty state | INV-AUDIT-04 |
| AI usage cap reviewed (`PAIMOS_AI_DAILY_CAP_TOKENS`, default 100k/user/day); admin override per user where needed | UI: `Settings → AI → Usage today` shows per-user totals | INV-AUDIT-05 |
| The `audit: ai_action ...` invariant is verified — no prompt or response body leaks into stdout | grep audit lines for substrings of known issue descriptions; expect zero hits | INV-AUDIT-02 |

### 3.6 · Secrets management

#### Field-level encryption at rest (PAI-261)

User-entered secrets stored in the SQLite DB (CRM provider tokens, the
OpenRouter `api_key`, future webhook secrets) are AES-256-GCM
encrypted with per-domain HKDF-derived subkeys. Both consumers go
through `backend/secretvault`; the master key resolves from
`PAIMOS_SECRET_KEY_FILE` > `PAIMOS_SECRET_KEY` compatibility env >
`$DATA_DIR/.secret-key` disk file.

There are two operationally distinct tiers — **pick one explicitly
based on your threat model**, don't drift between them by accident:

**Tier 1 — tamper-evident (default; suitable for dev / single-node).**
The master key auto-generates to `$DATA_DIR/.secret-key` (mode 0600)
on first boot. Encrypted values are unreadable without the key. **Does
NOT protect against** stolen backup tarballs or volume snapshots,
because the key sits next to the ciphertext on the same volume — anyone
who can read the data dir can read both. Defends against application-
layer file leaks, casual peeks, and "I dumped the DB to look at it"
scenarios.

**Tier 2 — operator-managed (recommended for production).** The master
key lives in your secret manager (sops / 1Password / k8s secret /
Vault) and ships into the container as an owner-only runtime file named by
`PAIMOS_SECRET_KEY_FILE` (containing base64 of 32 bytes). The key never lands
on the data volume or in the process environment; backup tarballs
(`scripts/deploy.sh` snapshots the data dir, not the runtime file) are useless
without the separately supplied key. This is the configuration your HARDENING
audit should aim for in production.

**Migrating T1 → T2.** Read the current bytes from
`$DATA_DIR/.secret-key`, base64-encode them into the secret-manager-owned file
named by `PAIMOS_SECRET_KEY_FILE`, and restart. The same key bytes — no
rotation needed — just moves the key outside the data volume. Optionally remove
`$DATA_DIR/.secret-key` after the runtime-file restart succeeds; the backend
prefers `_FILE` over the direct environment and data-directory forms.

**Rotation.** Both tiers support rotation via the
`paimos secrets rotate --new-key <base64> [--dry-run]` operator
subcommand (PAI-261 phase 3). Stop the service, run rotate (atomic
across all rows in a single transaction — partial failure rolls back
cleanly), replace the file named by `PAIMOS_SECRET_KEY_FILE` (or update the
compatibility environment / `.secret-key` forms),
restart. Today, swapping the master key without running rotate
corrupts every existing ciphertext — don't do that.

| Item | Verification | Invariant |
|---|---|---|
| `PAIMOS_SECRET_KEY_FILE` points to a readable secret-manager runtime file (Tier 2); direct `PAIMOS_SECRET_KEY` and `$DATA_DIR/.secret-key` are absent | `docker compose exec paimos sh -c 'test -n "${PAIMOS_SECRET_KEY_FILE:-}" && test -z "${PAIMOS_SECRET_KEY+x}" && test -r "$PAIMOS_SECRET_KEY_FILE"'` succeeds without printing the path or value; verify `.secret-key` is absent from the data volume separately | INV-PROV-01 |
| Master key documented in your operations log (which tier, where the bytes live, who can rotate) | operations log section "PAIMOS secrets" | – |
| Rotation cadence chosen and recorded (PAIMOS recommends annual minimum; sooner if ever exposed in logs / shells / shoulder-surfing) | rotation entries in the operations log; each entry includes the dry-run output that proved the rotation succeeded | – |
| `OIDC_CLIENT_SECRET_FILE` and `SMTP_PASS_FILE` use secret-manager runtime files; MinIO credentials are sourced from the secret manager; no values live in a checked-in `.env` file | `git log` of your deployment repo shows no `*_SECRET` / `*_PASSWORD` values; value-free runtime checks confirm the PAIMOS `_FILE` bindings are readable | INV-PROV-01 / 02 / 03 |
| Rotation cadence: OIDC client secret + SMTP creds + MinIO secret rotated **at least quarterly** unless your provider requires otherwise | rotation entries in the operations log | – |
| API keys (PAIMOS-issued, `paimos_…`) audited every six months; unused keys revoked | UI: `Settings → Users → <user> → API keys`; the recent `last_used_at` column drives the decision | INV-AUTH-03 |
| `ADMIN_PASSWORD` / `ADMIN_PASSWORD_FILE` **never re-set** after first boot (they are first-run-only and ignored if `admin` exists) | the value-free absence check from §3.2 succeeds | INV-AUTH-01 |
| No secret in CI logs, container env dumps, or commit history | `docker compose config` (resolved compose file) does not echo any secret in plain text; CI uses masked vars | – |

### 3.7 · Backups, restore, and disaster recovery

| Item | Verification | Reference |
|---|---|---|
| Backup runs on every deploy via `scripts/deploy.sh`; tarball lands in `$BACKUP_ROOT/<UTC-timestamp>/` | inspect the host: `ls $BACKUP_ROOT \| tail -5` shows recent backups | [`DEPLOY.md`](DEPLOY.md) |
| Backup destination is **off-host** (separate disk minimum; off-site preferred) | the host losing its primary disk does not also lose its backups | – |
| Backups include `data.tar.gz` + `manifest.yaml` + the prior `docker-compose.yml` | inspect any recent backup directory | [`DEPLOY.md`](DEPLOY.md) |
| **Restore tested** — at least one successful restore drill against a non-production target | drill recorded in the operations log per [`INCIDENT_RESPONSE.md` §3.3](INCIDENT_RESPONSE.md) | INV-EXPORT-04 |
| RPO target documented (PAIMOS default: 24h ≈ deploy cadence) | operations log | – |
| RTO target documented (PAIMOS default: ~5 min for image-pin rollback; ~15-30 min for full DB restore from tarball) | operations log | – |
| Hard-delete (purge) reviewed as irreversible — operators understand the asymmetry | UI: `Settings → Trash → Purge` is admin-only | INV-EXPORT-04 |

---

## 4 · Environment differences

The same checklist applies to dev / staging / production with these explicit relaxations / tightenings:

| Item | Dev | Staging | Production |
|---|---|---|---|
| TLS termination | optional (`http://localhost:8888`) | required | required |
| `COOKIE_SECURE=true` | unset OK | required | required |
| Initial admin password rotation | not required | required | required immediately |
| TOTP for admin users | recommended | required | required |
| Reverse proxy | optional | required | required |
| `PAIMOS_DEV_MODE=true` (logs reset links to stdout) | OK | **never** | **never** |
| `ADMIN_PASSWORD` / `ADMIN_PASSWORD_FILE` post-boot | may persist for tear-down convenience | removed | removed |
| Audit log forwarding | optional | optional | required |
| Backup destination off-host | not required | required | off-host **and** off-site |
| Restore drill cadence | not required | every 6 months | every 6 months **and** before any risky migration |
| OpenRouter API key visibility | OK to share among devs | scoped to staging | production-only key with separate billing |

The sharpest distinction is `PAIMOS_DEV_MODE=true`: it logs password-reset links to stdout, which makes local development livable when SMTP isn't configured. Per [`CONFIGURATION.md`](CONFIGURATION.md): **never set this in shared staging or production** — the link is a one-shot password reset and anyone with log access can use it.

---

## 5 · Verification — full deployment audit

A one-pass external audit of a hardened deployment. Run from a clean shell against the live host.

```sh
# 1 · TLS posture
echo "QUIT" | openssl s_client -connect your.host:443 -tls1_2 2>/dev/null | grep -E "Protocol|Cipher"
curl -sI https://your.host/api/health | grep -iE "strict-transport-security|x-frame-options|x-content-type-options|referrer-policy|permissions-policy"

# 2 · Auth posture
# Confirm rate limit kicks in
for i in $(seq 1 6); do
  curl -s -o /dev/null -w "%{http_code}\n" -X POST -H 'Content-Type: application/json' \
    https://your.host/api/auth/login -d '{"username":"admin","password":"wrong"}'
done
# Expect: 401 401 401 401 401 429

# 3 · No secrets in container env
docker compose exec paimos env | grep -iE "(password|secret|key|token)" | grep -v "PAIMOS_"
# Expect: empty (or only documented non-secret entries)

# 4 · Audit forwarding
docker compose logs --tail=200 paimos | grep -E "^audit:" | head -5
# Expect: lines flowing; reach into your aggregator and confirm ingest

# 5 · Backup verification
ls -lt $BACKUP_ROOT | head -5
gzip -t $BACKUP_ROOT/$(ls -t $BACKUP_ROOT | head -1)/data.tar.gz && echo "backup integrity OK"

# 6 · Provider verifiability
cosign verify ghcr.io/inspr-at/paimos:$(docker compose exec paimos cat /app/VERSION) \
  --certificate-identity-regexp '^https://github.com/inspr-at/paimos/.+' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
# Expect: signature verified

# 7 · API openapi presence
curl -sf https://your.host/api/openapi.json | jq '.info.version'
# Expect: matches your VERSION

# 8 · Schema introspection (no auth needed)
curl -sf https://your.host/api/schema | jq '.statuses, .priorities, .types'
# Expect: enum lists

# 9 · CSP-Report-Only is logged or accepted (depending on your sink)
curl -sI https://your.host/api/health | grep -i csp
# Expect: Content-Security-Policy-Report-Only with self-only

# 10 · Health check
curl -sf https://your.host/api/health | jq '.status'
# Expect: "ok"
```

If every command above returns the expected output, the deployment passes the §3 hardening checklist at a structural level. **Operational hardening** (per-user TOTP, project-access review, secret-manager hygiene) requires manual review of the items in §3 that aren't externally observable.

---

## 6 · Common configuration mistakes

Five mistakes operators consistently make. Each one has bitten at least one deployment in this project's history (or a comparable project) — none are theoretical.

1. **Reverse proxy strips the rate-limiter's IP signal.** PAIMOS rate-limits per IP+identity (INV-AUTH-04). If the reverse proxy doesn't forward the original client IP via `X-Forwarded-For` (or whatever header your deployment trusts), every brute-force attempt looks like it comes from the proxy itself — and the rate limit is shared across all users. Fix: configure the proxy to forward the real client IP, and configure PAIMOS / your wrapper to trust it.

2. **`COOKIE_SECURE=true` set without HTTPS.** Browser silently refuses to set the cookie. User logs in, gets a 200, no cookie, next request is unauthenticated, login loop. Fix: only set `COOKIE_SECURE=true` once HTTPS is verified working end-to-end, OR test with the cookie as `Secure; HttpOnly; SameSite=Lax` from the proxy side.

3. **`PAIMOS_DEV_MODE=true` left on in staging.** Password-reset links land in `docker compose logs`. Anyone with read-access to the log aggregator can reset any user's password. Fix: set this **only on localhost dev**; never on shared / multi-user instances. Documented as such in [`CONFIGURATION.md`](CONFIGURATION.md).

4. **Backups stored on the same disk as `$DATA_DIR`.** A failed disk takes both the live data and its backups. Fix: backups land on a separate disk minimum, off-site preferred. The deploy script's `$BACKUP_ROOT` should not be a path under `$DATA_DIR`'s parent.

5. **OpenRouter API key shared across dev / staging / prod.** Token usage in dev pollutes prod's daily cap; a leak in dev exposes the prod key. Fix: separate OpenRouter accounts per environment, or at minimum separate keys with separate billing limits per environment.

---

## 7 · Cross-references

- **[`THREAT_MODEL.md`](THREAT_MODEL.md)** — the model this guide hardens against. §4 invariants are the source of truth; the §3 checklist items here are the operator-side checks for those invariants.
- **[`CONFIGURATION.md`](CONFIGURATION.md)** — every env var, every operator knob.
- **[`DEPLOY.md`](DEPLOY.md)** — release + deploy + rollback runbook.
- **[`BACKUP_RESTORE.md`](BACKUP_RESTORE.md)** — backup scope, restore runbooks (full / forensic), captured drill, RPO/RTO targets; the §3.7 checklist row above is operationalised there.
- **[`INCIDENT_RESPONSE.md`](INCIDENT_RESPONSE.md)** — when something gets through the hardening; runbooks per incident class.
- **[`CONTINUITY.md`](CONTINUITY.md)** — bus-factor planning if the maintainer is out.
- **[`SECURITY.md`](../SECURITY.md)** — inbound disclosure policy.
- **[`claim-matrix.md`](claim-matrix.md)** — claim ↔ shipped-evidence registry; the basis for §3.5 / §3.6 verifications.
- **[`paimos.inspr.at/#trust`](https://paimos.inspr.at/#trust)** — public outward trust posture and verifiability claims this guide operationalises.
- **[`SECURITY_REVIEW.md`](SECURITY_REVIEW.md)** — the build-side companion: which CI scanners run, at what threshold, and the security-sensitive code-review rules. The §3.5 / §3.6 hardening rows here are operator-side; SECURITY_REVIEW.md covers the maintainer-side checks that prevent regressions.
- **[`REFERENCE_DEPLOYMENTS.md`](REFERENCE_DEPLOYMENTS.md)** — the production-validation register. The hardening checklist items in this guide map to validated-or-not state in the §4 matrix there; the F-08 finding (off-host backup destination) is the operator-responsibility row from §3.7 here, observed in production.
- **[`SECURITY_GOVERNANCE.md`](SECURITY_GOVERNANCE.md)** — the operating system for this checklist's review cadence; §1 names this guide's review as a recurring control, §4 puts the next review date on the unified calendar.
- **`scripts/check-claims.sh`** — release-time gate that enforces every public claim has shipped evidence.
