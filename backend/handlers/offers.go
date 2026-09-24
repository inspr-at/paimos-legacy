// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

type OfferBlock struct {
	Heading string          `json:"heading"`
	Body    string          `json:"body"`
	Nodes   []OfferTextNode `json:"nodes,omitempty"`
}
type OfferSender struct {
	Company       string `json:"company"`
	Street        string `json:"street"`
	PostalCode    string `json:"postal_code"`
	City          string `json:"city"`
	Country       string `json:"country"`
	RegisterNo    string `json:"register_no"`
	RegisterCourt string `json:"register_court"`
	Email         string `json:"email"`
	Phone         string `json:"phone"`
	Website       string `json:"website"`
	UID           string `json:"uid"`
	BankName      string `json:"bank_name"`
	IBAN          string `json:"iban"`
	BIC           string `json:"bic"`
	ContactPerson string `json:"contact_person"`
}
type OfferDefaults struct {
	Intro      string       `json:"intro"`
	Blocks     []OfferBlock `json:"blocks"`
	AcceptText string       `json:"accept_text"`
	VATNote    string       `json:"vat_note"`
}
type OfferSettings struct {
	Sender   OfferSender   `json:"sender"`
	Defaults OfferDefaults `json:"defaults"`
}
type OfferPosition struct {
	ShortText      string  `json:"short_text"`
	LongText       string  `json:"long_text"`
	Quantity       float64 `json:"quantity"`
	Unit           string  `json:"unit"`
	UnitPriceCents int64   `json:"unit_price_cents"`
	TotalCents     int64   `json:"total_cents"`
}
type OfferCustomer struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	Contact    string `json:"contact"`
	Country    string `json:"country"`
	CustomerNo string `json:"customer_no"`
	Email      string `json:"email,omitempty"`
}

// The first delivery stores the versioned document as one atomic aggregate.
// Positions and snapshots cannot become inconsistent with its revision or totals.
type OfferDocument struct {
	Title      string        `json:"title"`
	Subtitle   string        `json:"subtitle"`
	ProjectRef string        `json:"project_ref"`
	OfferDate  string        `json:"offer_date"`
	ValidUntil string        `json:"valid_until"`
	Sender     OfferSender   `json:"sender"`
	Customer   OfferCustomer `json:"customer"`
	OfferDefaults
	Positions     []OfferPosition    `json:"positions"`
	NetTotalCents int64              `json:"net_total_cents"`
	Footer        *OfferFooterLayout `json:"footer,omitempty"`
}

// OfferFooterLayout stores the centered footer mark. Absent means the legacy CSS lockup.
// Width 18–96mm. Offset -6..10mm: negative lifts the mark within the 7mm gap above the rule, positive lowers it inside the 16mm bottom padding.
type OfferFooterLayout struct {
	LogoWidthMM  float64 `json:"logo_width_mm"`
	LogoOffsetMM float64 `json:"logo_offset_mm"`
}
type Offer struct {
	Deleted         bool               `json:"deleted"`
	ID              int64              `json:"id"`
	OfferNo         string             `json:"offer_no"`
	CustomerID      int64              `json:"customer_id"`
	Status          string             `json:"status"`
	Revision        int64              `json:"revision"`
	Document        OfferDocument      `json:"document"`
	CreatedAt       string             `json:"created_at"`
	UpdatedAt       string             `json:"updated_at"`
	DocumentSHA256  string             `json:"document_sha256,omitempty"`
	Confirmation    *OfferConfirmation `json:"confirmation,omitempty"`
	SentAt          *string            `json:"sent_at"`
	PublicToken     string             `json:"public_token,omitempty"`
	AcceptedAt      *string            `json:"accepted_at,omitempty"`
	AcceptedName    string             `json:"accepted_name,omitempty"`
	AcceptedCompany string             `json:"accepted_company,omitempty"`
	AcceptedNote    string             `json:"accepted_note,omitempty"`
}

func loadOfferSettings() (OfferSettings, error) {
	var s OfferSettings
	s.Defaults = OfferDefaults{Blocks: []OfferBlock{}, VATNote: "exklusive 20 % USt"}
	for key, target := range map[string]any{"offer_sender": &s.Sender, "offer_defaults": &s.Defaults} {
		var raw string
		err := db.DB.QueryRow(`SELECT value FROM app_settings WHERE key=?`, key).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return s, err
		}
		if err = json.Unmarshal([]byte(raw), target); err != nil {
			return s, err
		}
	}
	return s, nil
}
func GetOfferSettings(w http.ResponseWriter, r *http.Request) {
	s, err := loadOfferSettings()
	if err != nil {
		jsonError(w, "Einstellungen konnten nicht geladen werden", 500)
		return
	}
	jsonOK(w, s)
}
func PutOfferSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OfferSettings
		ProseWriterVersion int `json:"prose_writer_version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&body); err != nil {
		jsonError(w, "Ungültige Einstellungen", 400)
		return
	}
	if err := validateOfferSender(body.Sender); err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	if len(body.Defaults.Blocks) > 20 {
		jsonError(w, "Maximal 20 Textbausteine", 400)
		return
	}
	current, err := loadOfferSettings()
	if err != nil {
		jsonError(w, "Einstellungen konnten nicht geladen werden", 500)
		return
	}
	if err = normalizeOfferBlocks(body.Defaults.Blocks); err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	if offerProseWriteUnsafe(current.Defaults.Blocks, body.Defaults.Blocks, body.ProseWriterVersion) {
		jsonError(w, offerProseStaleWriterMessage, http.StatusConflict)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "Speichern fehlgeschlagen", 500)
		return
	}
	defer tx.Rollback()
	for key, value := range map[string]any{"offer_sender": body.Sender, "offer_defaults": body.Defaults} {
		raw, e := json.Marshal(value)
		if e != nil {
			jsonError(w, "Ungültige Einstellungen", 400)
			return
		}
		if _, e = tx.Exec(`INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,datetime('now')) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, string(raw)); e != nil {
			jsonError(w, "Speichern fehlgeschlagen", 500)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		jsonError(w, "Speichern fehlgeschlagen", 500)
		return
	}
	jsonOK(w, body.OfferSettings)
}
func validateOfferSender(s OfferSender) error {
	if strings.TrimSpace(s.Company) == "" || strings.TrimSpace(s.Street) == "" || strings.TrimSpace(s.PostalCode) == "" || strings.TrimSpace(s.City) == "" || strings.TrimSpace(s.Country) == "" {
		return errors.New("Firma, Straße, PLZ, Ort und Land sind erforderlich")
	}
	a, err := mail.ParseAddress(s.Email)
	if err != nil || a.Address != s.Email {
		return errors.New("Gültige E-Mail-Adresse erforderlich")
	}
	iban := strings.ToUpper(strings.ReplaceAll(s.IBAN, " ", ""))
	if iban == "" {
		return nil
	}
	if len(iban) < 15 || len(iban) > 34 {
		return errors.New("Ungültige IBAN")
	}
	if iban[0] < 'A' || iban[0] > 'Z' || iban[1] < 'A' || iban[1] > 'Z' || iban[2] < '0' || iban[2] > '9' || iban[3] < '0' || iban[3] > '9' {
		return errors.New("Ungültige IBAN")
	}
	n := 0
	for _, c := range iban[4:] + iban[:4] {
		if c >= '0' && c <= '9' {
			n = (n*10 + int(c-'0')) % 97
		} else if c >= 'A' && c <= 'Z' {
			n = (n*100 + int(c-'A') + 10) % 97
		} else {
			return errors.New("Ungültige IBAN")
		}
	}
	if n != 1 {
		return errors.New("Ungültige IBAN")
	}
	return nil
}
func calculateOffer(d *OfferDocument, final bool) error {
	if len(d.Positions) > 100 || len(d.Blocks) > 20 {
		return errors.New("Zu viele Positionen oder Textbausteine")
	}
	if err := normalizeOfferBlocks(d.Blocks); err != nil {
		return err
	}
	if err := normalizeOfferFooter(d); err != nil {
		return err
	}
	day, err := time.Parse("2006-01-02", d.OfferDate)
	if err != nil {
		return errors.New("Ungültiges Angebotsdatum")
	}
	until, err := time.Parse("2006-01-02", d.ValidUntil)
	if err != nil || until.Before(day) {
		return errors.New("Gültig bis muss am oder nach dem Angebotsdatum liegen")
	}
	d.NetTotalCents = 0
	for i := range d.Positions {
		p := &d.Positions[i]
		if math.IsNaN(p.Quantity) || math.IsInf(p.Quantity, 0) || p.Quantity < 0 || p.Quantity > 1e6 || p.UnitPriceCents < 0 || p.UnitPriceCents > 1e9 {
			return errors.New("Menge oder Preis außerhalb des zulässigen Bereichs")
		}
		hundredths := math.Round(p.Quantity * 100)
		if math.Abs(p.Quantity*100-hundredths) > 0.000001 {
			return errors.New("Mengen dürfen höchstens zwei Nachkommastellen haben")
		}
		p.TotalCents = (int64(hundredths)*p.UnitPriceCents + 50) / 100
		d.NetTotalCents += p.TotalCents
		if d.NetTotalCents > 1e12 {
			return errors.New("Angebotssumme zu groß")
		}
		if final && (strings.TrimSpace(p.ShortText) == "" || p.Quantity <= 0) {
			return errors.New("Jede Position braucht eine Leistung und eine positive Menge")
		}
	}
	if final {
		if strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.Customer.Name) == "" || strings.TrimSpace(d.Customer.Address) == "" || len(d.Positions) == 0 {
			return errors.New("Titel, Kundenanschrift und mindestens eine Position sind erforderlich")
		}
		if !validOfferEmail(d.Sender.Email) {
			return errors.New("Gültige Absender-E-Mail ist erforderlich")
		}
		if !validOfferEmail(d.Customer.Email) {
			return errors.New("Gültige E-Mail des Kundenkontakts ist erforderlich")
		}
		if err := validateOfferSender(d.Sender); err != nil {
			return err
		}
	}
	return nil
}

func canonFooterMM(value, min, max float64) (float64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("Fußzeilenlogo außerhalb des zulässigen Bereichs")
	}
	rounded := math.Round(value*10) / 10
	if rounded < min || rounded > max {
		return 0, errors.New("Fußzeilenlogo außerhalb des zulässigen Bereichs")
	}
	return rounded, nil
}

func normalizeOfferFooter(d *OfferDocument) error {
	if d.Footer == nil {
		return nil
	}
	width, err := canonFooterMM(d.Footer.LogoWidthMM, 18, 96)
	if err != nil {
		return err
	}
	offset, err := canonFooterMM(d.Footer.LogoOffsetMM, -6, 10)
	if err != nil {
		return err
	}
	d.Footer.LogoWidthMM = width
	d.Footer.LogoOffsetMM = offset
	return nil
}

func scanOffer(row rowScanner) (Offer, error) {
	var o Offer
	var raw string
	err := row.Scan(&o.ID, &o.OfferNo, &o.CustomerID, &o.Status, &o.Revision, &raw, &o.CreatedAt, &o.UpdatedAt, &o.SentAt, &o.PublicToken, &o.AcceptedAt, &o.AcceptedName, &o.AcceptedCompany, &o.AcceptedNote, &o.Deleted)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &o.Document)
		if err == nil && o.Status == "sent" && (!offerDateValid(o.Document.ValidUntil) || o.Document.ValidUntil < offerToday()) {
			o.Status = "expired"
		}
	}
	return o, err
}

const offerColumns = `id,offer_no,customer_id,status,revision,document,created_at,updated_at,sent_at,COALESCE(public_token,''),accepted_at,accepted_name,accepted_company,accepted_note,EXISTS(SELECT 1 FROM offer_visibility WHERE offer_id=offers.id AND deleted_at IS NOT NULL)`

func GetOffer(w http.ResponseWriter, r *http.Request) {
	o, err := scanOffer(db.DB.QueryRow(`SELECT `+offerColumns+` FROM offers WHERE id=?`, chi.URLParam(r, "id")))
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "Angebot nicht gefunden", 404)
		return
	}
	if err != nil {
		jsonError(w, "Laden fehlgeschlagen", 500)
		return
	}
	if o.Status == "draft" {
		_ = db.DB.QueryRowContext(r.Context(), `SELECT contact_email FROM customers WHERE id=?`, o.CustomerID).Scan(&o.Document.Customer.Email)
	}
	loadOfferConfirmation(r.Context(), &o)
	jsonOK(w, o)
}
func ListCustomerOffers(w http.ResponseWriter, r *http.Request) {
	rows, err := db.DB.Query(`SELECT `+offerColumns+` FROM offers WHERE customer_id=? AND (? OR NOT EXISTS(SELECT 1 FROM offer_visibility WHERE offer_id=offers.id AND deleted_at IS NOT NULL)) ORDER BY id DESC`, chi.URLParam(r, "id"), r.URL.Query().Get("include_deleted") == "1")
	if err != nil {
		jsonError(w, "Laden fehlgeschlagen", 500)
		return
	}
	defer rows.Close()
	out := []Offer{}
	for rows.Next() {
		o, e := scanOffer(rows)
		if e != nil {
			jsonError(w, "Laden fehlgeschlagen", 500)
			return
		}
		out = append(out, o)
	}
	if rows.Err() != nil {
		jsonError(w, "Laden fehlgeschlagen", 500)
		return
	}
	jsonOK(w, out)
}
func nextOfferSequence(tx *sql.Tx, key string) (int, error) {
	var n int
	err := tx.QueryRow(`INSERT INTO offer_sequences(key,value) VALUES(?,1) ON CONFLICT(key) DO UPDATE SET value=value+1 RETURNING value`, key).Scan(&n)
	return n, err
}
func CreateOffer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CustomerID         int64          `json:"customer_id"`
		DuplicateID        int64          `json:"duplicate_id"`
		ProseWriterVersion int            `json:"prose_writer_version"`
		Document           *OfferDocument `json:"document,omitempty"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		jsonError(w, "Ungültiges Angebot", 400)
		return
	}
	// Create copies stored defaults or the duplicated offer. It does not apply a
	// client document. Newer prose in that document is refused when the client
	// does not name the current writer; a legacy body has neither and still creates.
	if body.Document != nil && offerProseWriteUnsafe(nil, body.Document.Blocks, body.ProseWriterVersion) {
		jsonError(w, offerProseStaleWriterMessage, http.StatusConflict)
		return
	}
	c := getCustomerByID(body.CustomerID)
	if c == nil {
		jsonError(w, "Kunde nicht gefunden", 404)
		return
	}
	s, err := loadOfferSettings()
	if err != nil {
		jsonError(w, "Einstellungen konnten nicht geladen werden", 500)
		return
	}
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		jsonError(w, "Zeitzone nicht verfügbar", 500)
		return
	}
	now := time.Now().In(loc)
	d := OfferDocument{Title: "Beratungsleistungen", OfferDate: now.Format("2006-01-02"), ValidUntil: now.AddDate(0, 0, 30).Format("2006-01-02"), Sender: s.Sender, OfferDefaults: s.Defaults, Positions: []OfferPosition{{Quantity: 1, Unit: "Pauschale"}}, Customer: OfferCustomer{Name: c.Name, Contact: c.ContactName, Email: c.ContactEmail, Address: c.Address, Country: c.Country}}
	if c.BillingAddressStreet != "" {
		d.Customer.Address = c.BillingAddressStreet + "\n" + strings.TrimSpace(c.BillingAddressZip+" "+c.BillingAddressCity)
		d.Customer.Country = c.BillingAddressCountry
	}
	if body.DuplicateID == 0 {
		d.Footer = &OfferFooterLayout{LogoWidthMM: 43.3, LogoOffsetMM: 2}
	}
	if body.DuplicateID != 0 {
		source, e := scanOffer(db.DB.QueryRow(`SELECT `+offerColumns+` FROM offers WHERE id=? AND customer_id=?`, body.DuplicateID, c.ID))
		if e != nil {
			jsonError(w, "Vorlage nicht gefunden", 404)
			return
		}
		date, until := d.OfferDate, d.ValidUntil
		d = source.Document
		d.OfferDate = date
		d.ValidUntil = until
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "Anlegen fehlgeschlagen", 500)
		return
	}
	defer tx.Rollback()
	n, err := nextOfferSequence(tx, "offer:"+now.Format("060102"))
	if err != nil {
		jsonError(w, "Nummernvergabe fehlgeschlagen", 500)
		return
	}
	var customerNo sql.NullString
	if err = tx.QueryRow(`SELECT customer_no FROM customers WHERE id=?`, c.ID).Scan(&customerNo); err != nil {
		jsonError(w, "Kunde nicht gefunden", 404)
		return
	}
	if !customerNo.Valid {
		number, e := nextCustomerNumber(tx, now)
		if e != nil {
			jsonError(w, "Nummernvergabe fehlgeschlagen", 500)
			return
		}
		customerNo.String = number
		if _, err = tx.Exec(`UPDATE customers SET customer_no=? WHERE id=?`, customerNo.String, c.ID); err != nil {
			jsonError(w, "Nummernvergabe fehlgeschlagen", 500)
			return
		}
	}
	d.Customer.CustomerNo = customerNo.String
	d.Customer.Email = c.ContactEmail
	if err = calculateOffer(&d, false); err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	raw, _ := json.Marshal(d)
	user := auth.GetUser(r)
	if user == nil {
		jsonError(w, "Anmeldung erforderlich", 401)
		return
	}
	res, err := tx.Exec(`INSERT INTO offers(offer_no,customer_id,document,created_by) VALUES(?,?,?,?)`, fmt.Sprintf("A%s-%02d", now.Format("060102"), n), c.ID, string(raw), user.ID)
	if err != nil {
		jsonError(w, "Anlegen fehlgeschlagen", 500)
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		jsonError(w, "Anlegen fehlgeschlagen", 500)
		return
	}
	if err = tx.Commit(); err != nil {
		jsonError(w, "Anlegen fehlgeschlagen", 500)
		return
	}
	o, err := scanOffer(db.DB.QueryRow(`SELECT `+offerColumns+` FROM offers WHERE id=?`, id))
	if err != nil {
		jsonError(w, "Laden fehlgeschlagen", 500)
		return
	}
	w.WriteHeader(201)
	jsonOK(w, o)
}
func PutOffer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Revision           int64         `json:"revision"`
		Document           OfferDocument `json:"document"`
		Finalize           bool          `json:"finalize"`
		ProseWriterVersion int           `json:"prose_writer_version"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&body) != nil || body.Revision < 1 {
		jsonError(w, "Ungültiges Angebot", 400)
		return
	}
	if body.Finalize {
		if err := db.DB.QueryRowContext(r.Context(), `SELECT contact_email FROM customers WHERE id=(SELECT customer_id FROM offers WHERE id=?)`, chi.URLParam(r, "id")).Scan(&body.Document.Customer.Email); err != nil {
			jsonError(w, "Kundenkontakt konnte nicht geladen werden", 400)
			return
		}
		body.Document.Customer.Email = strings.TrimSpace(body.Document.Customer.Email)
	}
	if err := calculateOffer(&body.Document, body.Finalize); err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	// The immutable customer number is loaded from this offer, never trusted from a draft client.
	var rawExisting string
	err := db.DB.QueryRow(`SELECT document FROM offers WHERE id=?`, chi.URLParam(r, "id")).Scan(&rawExisting)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "Angebot nicht gefunden", 404)
		return
	}
	if err != nil {
		jsonError(w, "Laden fehlgeschlagen", 500)
		return
	}
	var existing OfferDocument
	if json.Unmarshal([]byte(rawExisting), &existing) != nil {
		jsonError(w, "Laden fehlgeschlagen", 500)
		return
	}
	body.Document.Customer.CustomerNo = existing.Customer.CustomerNo
	if offerProseWriteUnsafe(existing.Blocks, body.Document.Blocks, body.ProseWriterVersion) {
		jsonError(w, offerProseStaleWriterMessage, http.StatusConflict)
		return
	}
	raw, _ := json.Marshal(body.Document)
	status := "draft"
	var sent any
	var token any
	if body.Finalize {
		if body.Document.ValidUntil < offerToday() {
			jsonError(w, "Die Bindefrist ist bereits abgelaufen", 400)
			return
		}
		generated, e := newOfferToken()
		if e != nil {
			jsonError(w, "Kundenlink konnte nicht erstellt werden", 500)
			return
		}
		token = generated
		status = "sent"
		sent = time.Now().UTC().Format(time.RFC3339)
	}
	o, err := scanOffer(db.DB.QueryRow(`UPDATE offers SET document=?,status=?,sent_at=?,public_token=COALESCE(?,public_token),revision=revision+1,updated_at=datetime('now') WHERE id=? AND revision=? AND status='draft' RETURNING `+offerColumns, string(raw), status, sent, token, chi.URLParam(r, "id"), body.Revision))
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "Das Angebot wurde inzwischen geändert oder bereits finalisiert. Bitte neu laden.", 409)
		return
	}
	if err != nil {
		jsonError(w, "Speichern fehlgeschlagen", 500)
		return
	}
	jsonOK(w, o)
}
