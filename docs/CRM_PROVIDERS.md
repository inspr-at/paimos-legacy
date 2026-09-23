# Adding a CRM Provider

PAIMOS owns its own customer data model (PAI-28). External CRMs
(HubSpot, Pipedrive, Salesforce, Attio, ...) are **optional** sync
sources, plugged in through a small Go interface or the HTTP sidecar
provider. This page is the developer guide for adding either shape.

> **Audience:** self-hosted PAIMOS maintainers and contributors who
> want to wire a CRM that doesn't ship in upstream. If you just want
> to *use* an existing provider (e.g. HubSpot), look in
> Settings → CRM in the app.

---

## 1. Why pluggable

PAIMOS designs for three audiences ([PAI-28]):

1. **No-CRM users** — manual customer entry is a primary mode, not a
   fallback. The plugin layer is opt-in. Instances that want no customer
   surface at all flip the instance switch under Integrations → CRM
   (`crm_enabled`, PAI-980): it hides the Customers entry points in every
   shell without touching data or routes.
2. **HubSpot users** — one-shot import + manual re-sync + deep-link
   via the in-tree HubSpot provider.
3. **Other-CRM users** — write a Go provider against the
   `crm.Provider` interface; no fork of core PAIMOS, no schema
   change.

Provider semantics live in one place: the registry. Adding HubSpot,
Pipedrive, or your internal CRM is the same shape — register a Go
type that satisfies `crm.Provider`, and the rest of the app
(sidebar, customer list, detail header, sync button, admin
Integrations tab) lights up via the existing
`useExternalProvider` composable.

[PAI-28]: https://pm.barta.cm/projects/PAI/issues/PAI-28

---

## 2. Interface walkthrough

The contract is in [`backend/handlers/crm/provider.go`](../backend/handlers/crm/provider.go).
Every method is exercised by the generic HTTP handlers in
`backend/handlers/crm/handlers.go`, so once your type satisfies the
interface, the API and UI work without further changes.

```go
type Provider interface {
    ID() string                                                      // stable; never rename once shipped
    Name() string                                                    // human display name
    LogoURL() string                                                 // path or URL; "" → globe fallback
    ConfigSchema() ConfigSchema                                      // fields the admin UI renders
    ValidateConfig(values map[string]string) error                   // surface errors to admin
    ImportRef(ctx, rawRef string, cfg ProviderConfig) (CustomerImport, error)
    Sync(ctx, externalID string, cfg ProviderConfig) (PartialUpdate, error)
    DeepLink(externalID string, cfg ProviderConfig) string
}
```

Two payload types:

- `CustomerImport` carries the field set the provider can populate on
  initial import (`Name`, `Industry`, `Address`, `Country`,
  `ContactName`, `ContactEmail`, plus `ExternalID` / `ExternalURL`).
  Empty strings = leave unset.
- `PartialUpdate` is what `Sync` returns — same fields in pointer
  form. The generic sync handler PATCHes only fields the provider
  actually wrote, so PAIMOS-only fields like `rate_hourly` and
  `notes` are **never** clobbered by an upstream change.

---

## 3. Skeleton: copy / paste / rename

Create `backend/handlers/crm/<provider>/provider.go`:

```go
// Package <provider> is a CRMProvider implementation for <CRM Name>.
package pipedrive

import (
    "context"
    "github.com/inspr-at/paimos/backend/handlers/crm"
)

func init() { crm.Register(&Provider{}) }

type Provider struct{}

func (p *Provider) ID() string      { return "pipedrive" }
func (p *Provider) Name() string    { return "Pipedrive" }
func (p *Provider) LogoURL() string { return "/assets/crm/pipedrive.svg" }

func (p *Provider) ConfigSchema() crm.ConfigSchema {
    return crm.ConfigSchema{Fields: []crm.ConfigField{
        {Key: "token",      Label: "API Token", Type: "secret", Required: true,
         Help: "Personal API token from Pipedrive → Settings → Personal preferences → API."},
        {Key: "company",    Label: "Company subdomain", Type: "string", Required: true,
         Help: "The <subdomain> in https://<subdomain>.pipedrive.com.",
         Placeholder: "acme"},
    }}
}

func (p *Provider) ValidateConfig(values map[string]string) error {
    if values["token"] == "" || values["company"] == "" {
        return errors.New("token and company subdomain are both required")
    }
    return nil
}

func (p *Provider) ImportRef(ctx context.Context, rawRef string, cfg crm.ProviderConfig) (crm.CustomerImport, error) {
    orgID, err := resolveOrgID(rawRef)
    if err != nil {
        return crm.CustomerImport{}, err
    }
    org, err := fetchOrg(ctx, cfg.Get("company"), cfg.Get("token"), orgID)
    if err != nil {
        return crm.CustomerImport{}, err
    }
    return crm.CustomerImport{
        Name:        org.Name,
        Industry:    org.Industry,
        Address:     org.Address,
        Country:     org.Country,
        ExternalID:  org.ID,
        ExternalURL: p.DeepLink(org.ID, cfg),
    }, nil
}

func (p *Provider) Sync(ctx context.Context, externalID string, cfg crm.ProviderConfig) (crm.PartialUpdate, error) {
    org, err := fetchOrg(ctx, cfg.Get("company"), cfg.Get("token"), externalID)
    if err != nil {
        return crm.PartialUpdate{}, err
    }
    return crm.PartialUpdate{
        Name:     &org.Name,
        Industry: &org.Industry,
        Address:  &org.Address,
        Country:  &org.Country,
    }, nil
}

func (p *Provider) DeepLink(externalID string, cfg crm.ProviderConfig) string {
    company := cfg.Get("company")
    if company == "" || externalID == "" {
        return ""
    }
    return fmt.Sprintf("https://%s.pipedrive.com/organization/%s", company, externalID)
}
```

Then blank-import the package from `backend/main.go` so its `init()`
fires before routes are registered:

```go
import (
    // CRM provider plugins. One blank import per compiled-in provider.
    _ "github.com/inspr-at/paimos/backend/handlers/crm/hubspot"
    _ "github.com/inspr-at/paimos/backend/handlers/crm/pipedrive"
)
```

That's it. The next `just deploy` ships your provider; the admin sees
a new card in Settings → CRM, configures it, enables it, and customer
imports route through it automatically.

---

## 4. Config schema field types

`ConfigField.Type` drives both rendering in the admin UI and the
storage path the plugin layer uses:

| Type     | Storage                      | Admin UI                                              |
|----------|------------------------------|-------------------------------------------------------|
| `string` | plain JSON, `config_json`    | `<input type="text">`                                 |
| `number` | plain JSON, `config_json`    | `<input type="number">`                               |
| `select` | plain JSON, `config_json`    | `<select>` populated from `Options`                   |
| `secret` | AES-GCM, `config_secret_json`| password input; never echoed; "Replace" / "Clear" UI  |

Only mark a field `secret` if it is actually a credential. Plain
strings are visible to the admin tab and to the diagnostic logs;
secrets are not.

---

## 5. Error handling conventions

Return `*crm.ProviderError` with a `Kind` from this set so the
generic handler maps to a sensible HTTP status:

| `Kind`                       | HTTP | When to use                                          |
|------------------------------|------|------------------------------------------------------|
| `ErrProviderUnreachable`     | 502  | Network failure; `http.Client` returned an error     |
| `ErrProviderAuth`            | 401  | Upstream returned 401 / 403                          |
| `ErrProviderNotFound`        | 404  | Upstream returned 404 for the requested entity       |
| `ErrProviderBadRequest`      | 400  | The user-supplied `ref` couldn't be parsed           |
| `ErrProviderUnknown` (zero)  | 500  | Anything else; details go to the server log only     |

The generic handler also accepts plain `error` values; those become
`500` with the error string surfaced to the admin. Prefer typed
errors so the UI can render differentiated messaging.

**Never** include the raw token / API key in an error message — the
HTTP layer surfaces the message string verbatim to the admin client.

---

## 6. Contract test harness

The shared provider-contract harness lives in
`backend/handlers/crm/contracttest`. HubSpot and the HTTP sidecar
provider both exercise it in their package tests, while keeping
provider-specific regression tests beside the implementation. The
harness:

- Boot a fake HTTP upstream
- Run your provider's `ImportRef` against it with a known reference
- Assert the returned `CustomerImport` matches a fixture
- Repeat for `Sync`
- Verify `DeepLink` is constructed with the expected pattern

To opt in, add a provider test that builds a fake upstream, prepares a
`contracttest.Fixture`, and calls
`contracttest.AssertProviderCoreFlows(t, &Provider{}, cfg, fixture)`.

---

## 7. HTTP sidecar providers

PAIMOS also ships a built-in `http` provider for non-Go integrations.
Instead of recompiling PAIMOS, run a small sidecar service in any
language and configure it in Integrations → CRM with:

- `base_url`: the sidecar root URL
- `hmac_secret`: the shared signing secret, encrypted at rest
- `timeout_seconds`: optional per-request timeout

The sidecar contract is documented in
[`CRM_HTTP_CONTRACT.md`](CRM_HTTP_CONTRACT.md), with a Draft 2020-12
schema in [`schemas/crm-http-v1.json`](schemas/crm-http-v1.json).
The contract covers:

- signed connection test metadata: `GET /v1/schema`
- customer import: `POST /v1/import`
- customer re-sync: `POST /v1/sync`
- remote search: `POST /v1/search`
- CRM deep links: `GET /v1/deep-link?id=...`

Requests are authenticated with
`X-Paimos-Timestamp` and `X-Paimos-Signature`, where the signature is
`hex(HMAC-SHA256(secret, timestamp + "\n" + raw_body))`. Sidecars
should reject timestamps outside a ±300 second window and compare
signatures in constant time.

A runnable minimal sidecar lives in
[`../examples/crm-http-sidecar/server.py`](../examples/crm-http-sidecar/server.py):

```bash
PAIMOS_CRM_HMAC_SECRET=dev-secret python3 examples/crm-http-sidecar/server.py
```

Then configure the HTTP CRM provider with:

```text
base_url: http://127.0.0.1:8089
hmac_secret: dev-secret
```

The current PAIMOS provider config model is keyed by provider id, so
v1 supports one configured HTTP sidecar per deployment. Multiple named
HTTP sidecars and inbound CRM push/webhooks are separate future
tickets.

[PAI-56]:  https://pm.barta.cm/projects/PAI/issues/PAI-56
[PAI-108]: https://pm.barta.cm/projects/PAI/issues/PAI-108


## Offers (PAI-991)

From the customer's **Angebote** section, choose **Angebot erstellen**. Configure
sender details and editable German text defaults under Integrations → CRM →
**Angebote: Absender & Textbausteine**, or in the editor's dialog. Saving those
defaults does not change an offer that is already open. No company,
UID or bank account is invented in backend defaults. Existing contacts are reused.
The suggested terms come from the supplied v8 prototype and are operator-editable.

Offers receive `A<YYMMDD>-<NN>` at draft creation; customers receive
`K<YYMM><N>` with their first offer: `K26091`, `K26092`, then `K26101` in October.
The unpadded customer counter restarts each Europe/Vienna calendar month.
Allocations are transactional, permanent and never reused. Draft offer gaps are intentional.
An admin may explicitly convert a legacy `K<YY>-<NNN>` number through
`POST /api/customers/{id}/number/reformat` with `expected_customer_no`, but only
while every related offer remains a draft. The customer and all draft snapshots
change atomically, and draft revisions advance so stale editors cannot restore
the old number. Finalized offers and existing monthly numbers cannot be renumbered.
The first version stores positions, sender, recipient and text as an atomic JSON
document with a revision, rather than a separate positions table. Quantities
have two decimal places; amounts are integer cents, rounded half-up per position
on the server. A stale revision returns 409 without changing the document.

**Finalisieren** freezes that document; it does not send email. **Duplizieren**
creates a new editable offer. **Druckansicht / PDF** opens an authenticated route
without application chrome; browser Print / Save as PDF exports the same document.
The v8 visual rules are preserved, with extra pages when the complete terms or
positions exceed A4. An indivisible item exceeding a whole page must be split or
shortened before printing. A text-block body may also carry optional `nodes`:
paragraphs and nested bullet or numbered items (depth 0–5). An item may set
`marker` to `disc`, `circle`, `square`, `dash` or `decimal`. Without `marker`,
an item keeps the depth glyph (•, ◦, ▪). Decimal numbers without further fields restart after a
paragraph or a non-numbered item at the same depth; nested numbers count on
their own level. `numbering: "outline"` renders multilevel decimals such as
3, 3.1, 3.1.1. `list_start` is a positive start value and `list_continue`
joins the preceding matching list even across a paragraph. Those fields are
not stored on a node created by splitting a line. Without `nodes`, `body` stays
literal plain text, including characters that look like Markdown. The editor,
public page and PDF share that markup. Optional `footer.logo_width_mm` (18–96)
and `footer.logo_offset_mm` (-6–10) size the centered footer mark and move it
up (negative) or down (positive). Absent `footer` keeps the previous lockup.
New offers start at 43.3 mm wide and 2 mm lower. Accepted and other non-draft
offers stay immutable. Draft section order is part of the saved document;
central text defaults are not copied into an offer that is already open. A block that is still taller than one
page keeps its full text and refuses print instead of clipping it. Headings,
addresses, the introduction and position lines stay plain. Settings preserve
`nodes` but do not edit them. Bundled fonts avoid external font dependencies.

API: admin `GET/PUT /api/integrations/crm/offers` stores `offer_sender` and
`offer_defaults`; authenticated `GET /api/customers/{id}/offers` and
`GET /api/offers/{id}` read documents. Admin `POST /api/offers` accepts
`customer_id` and optional `duplicate_id`; admin `PUT /api/offers/{id}` accepts
`revision`, `document` and optional `finalize`. The CRM module switch follows
PAI-980 (UI reachability; data/API remain available).
Finalization also creates a 32-byte random customer capability. **Kundenlink
kopieren** copies `/offers/<token>`; existing finalized offers can enable their
link through the same admin action. The last printed page includes a 26 mm SVG
QR code with a four-module quiet zone. No third-party QR service sees the link.

The customer can read and print without login and explicitly accept with name,
company, confirmation and an optional note. One transaction changes status and
writes an immutable receipt with UTC time, IP/browser metadata, the frozen
JSON document hash and its revision. Repeated, stale or expired acceptance gets
409 and cannot overwrite the first signer. Creator-specific accepted-offer
notices are derived durably from these receipts and shown when opening CRM;
there is no email dispatch or general notification service in this slice.

`GET /api/public/offers/{token}` returns the document and receipt only;
`POST .../accept` requires JSON, `X-Offer-Acceptance: 1`, `revision`, `name`,
`company`, `confirmed: true` and optional `note`. Token/IP limits return 429.
Unknown and draft links return the same 404. Unlike the existing internal CRM
APIs, the public surface also closes when `crm_enabled=0`. Public SPA/API
responses use no-store, no-referrer and noindex; application access/session logs
exclude capability URLs. Reverse-proxy access logging must likewise avoid
recording these URLs. `OFFER_TRUSTED_PROXY_CIDRS` is a comma-separated allowlist
of immediate reverse proxies; unset means all forwarding headers are ignored.
The limiter and audit use the same address: the nearest untrusted hop in a
trusted proxy's X-Forwarded-For chain, otherwise the TCP peer. PMA uses the
verified Docker bridge gateway (172.17.0.1/32), with Caddy as the only public
entry point and the container published on host loopback only.

`valid_until` includes the entire Europe/Vienna calendar day. Subsequent reads
project unaccepted sent offers as expired, and the acceptance transaction checks
the deadline again. Expired offers remain readable but have no acceptance form.
Manual acceptance/decline controls and server-generated PDFs remain deferred.
