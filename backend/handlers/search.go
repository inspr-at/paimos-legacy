// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package handlers

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

// keyPattern matches a full issue key like "ACME-1" or "ACME-1"
var keyPattern = regexp.MustCompile(`(?i)^([A-Z][A-Z0-9]+)-(\d+)$`)

// keyPrefixPattern matches queries that start with an issue key, e.g. "ACME-1 crash"
var keyPrefixPattern = regexp.MustCompile(`(?i)^([A-Z][A-Z0-9]+-\d+)\s+(.+)$`)

// projKeyPattern matches a bare project key (no hyphen), e.g. "ACME"
var projKeyPattern = regexp.MustCompile(`(?i)^([A-Z][A-Z0-9]+)$`)

const issueSelectCols = `i.id, i.issue_number, i.title, i.type, i.status, i.priority, i.project_id,
		       COALESCE(p.key, ''),
		       ` + costUnitLabelExpr + `, ` + releaseLabelExpr + `,
		       i.assignee_id, COALESCE(u.username, '')`

// SearchResults is the response shape for GET /api/search.
// Issues is a flat, deduplicated list from all match paths (FTS content,
// tag matches, assignee matches, comment matches). Default limit 100, paginated
// via ?offset. HasMore indicates more results exist beyond the returned page.
type SearchResults struct {
	Projects []SearchProject `json:"projects"`
	Issues   []SearchIssue   `json:"issues"`
	Users    []SearchUser    `json:"users"`
	Tags     []models.Tag    `json:"tags"`
	HasMore  bool            `json:"has_more"`
}

type SearchProject struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	Key         string       `json:"key"`
	Description string       `json:"description"`
	Status      string       `json:"status"`
	Tags        []models.Tag `json:"tags"`
}

type SearchIssue struct {
	ID               int64   `json:"id"`
	IssueKey         string  `json:"issue_key"`
	Title            string  `json:"title"`
	Type             string  `json:"type"`
	Status           string  `json:"status"`
	Priority         string  `json:"priority"`
	ProjectID        *int64  `json:"project_id"`
	ProjectKey       string  `json:"project_key"`
	CostUnit         string  `json:"cost_unit"`
	Release          string  `json:"release"`
	AssigneeID       *int64  `json:"assignee_id"`
	AssigneeUsername *string `json:"assignee_username"`
}

type SearchUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// issueDedup tracks seen issue IDs to produce a flat deduplicated list.
type issueDedup struct {
	seen []int64
	list []SearchIssue
}

func (d *issueDedup) add(iss SearchIssue) {
	for _, id := range d.seen {
		if id == iss.ID {
			return
		}
	}
	d.seen = append(d.seen, iss.ID)
	d.list = append(d.list, iss)
}

func (d *issueDedup) addAll(issues []SearchIssue) {
	for _, iss := range issues {
		d.add(iss)
	}
}

// result returns a page of the deduplicated list.
// Collects up to limit+1 items — if more than limit exist, HasMore = true.
// offset skips the first N results (for load-more pagination).
func (d *issueDedup) result(offset, limit int) ([]SearchIssue, bool) {
	all := d.list
	if all == nil {
		return []SearchIssue{}, false
	}
	// Apply offset
	if offset > len(all) {
		return []SearchIssue{}, false
	}
	all = all[offset:]
	// Check has_more by peeking one beyond the limit
	hasMore := len(all) > limit
	if hasMore {
		all = all[:limit]
	}
	return all, hasMore
}

// scanIssueRows scans all rows from a standard issueSelectCols query.
func scanIssueRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
}) []SearchIssue {
	defer rows.Close()
	var out []SearchIssue
	for rows.Next() {
		var iss SearchIssue
		var num int
		var projKey string
		if err := rows.Scan(&iss.ID, &num, &iss.Title, &iss.Type, &iss.Status, &iss.Priority,
			&iss.ProjectID, &projKey, &iss.CostUnit, &iss.Release, &iss.AssigneeID, &iss.AssigneeUsername); err == nil {
			iss.ProjectKey = projKey
			if projKey != "" && num > 0 {
				iss.IssueKey = projKey + "-" + itoa(num)
			} else if iss.ProjectID == nil {
				// Sprint or other project-less issue
				iss.IssueKey = "SPRINT-" + itoa(int(iss.ID))
			}
			out = append(out, iss)
		}
	}
	return out
}

// lookupIssueByKey fetches a single issue by project key + issue number.
func lookupIssueByKey(projKey string, issueNum int) (SearchIssue, bool) {
	row := db.DB.QueryRow(`
		SELECT `+issueSelectCols+`
		FROM issues i
		JOIN projects p ON p.id = i.project_id
		LEFT JOIN users u ON u.id = i.assignee_id
		WHERE UPPER(p.key) = UPPER(?) AND i.issue_number = ? AND i.deleted_at IS NULL
	`, projKey, issueNum)
	var iss SearchIssue
	var num int
	var pk string
	if err := row.Scan(&iss.ID, &num, &iss.Title, &iss.Type, &iss.Status, &iss.Priority,
		&iss.ProjectID, &pk, &iss.CostUnit, &iss.Release, &iss.AssigneeID, &iss.AssigneeUsername); err != nil {
		return iss, false
	}
	iss.ProjectKey = pk
	if pk != "" && num > 0 {
		iss.IssueKey = pk + "-" + itoa(num)
	}
	return iss, true
}

// lookupIssuesByProjKey fetches all issues for a project key.
func lookupIssuesByProjKey(projKey string) []SearchIssue {
	rows, err := db.DB.Query(`
		SELECT `+issueSelectCols+`
		FROM issues i
		JOIN projects p ON p.id = i.project_id
		LEFT JOIN users u ON u.id = i.assignee_id
		WHERE UPPER(p.key) = UPPER(?) AND i.deleted_at IS NULL
		ORDER BY i.issue_number
		LIMIT 201
	`, projKey)
	if err != nil {
		return nil
	}
	return scanIssueRows(rows)
}

// lookupIssuesByKeyPrefix finds issues where issue_number starts with the given digit prefix.
// E.g. projKey="ACME", numPrefix="15" matches issue_number 15, 150, 151, 1500, etc.
func lookupIssuesByKeyPrefix(projKey string, numPrefix string) []SearchIssue {
	rows, err := db.DB.Query(`
		SELECT `+issueSelectCols+`
		FROM issues i
		JOIN projects p ON p.id = i.project_id
		LEFT JOIN users u ON u.id = i.assignee_id
		WHERE UPPER(p.key) = UPPER(?)
		  AND CAST(i.issue_number AS TEXT) LIKE ?
		  AND CAST(i.issue_number AS TEXT) != ?
		  AND i.deleted_at IS NULL
		ORDER BY i.updated_at DESC
		LIMIT 20
	`, projKey, numPrefix+"%", numPrefix)
	if err != nil {
		return nil
	}
	return scanIssueRows(rows)
}

const searchDefaultLimit = 100
const searchMaxLimit = 200

func Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	issueTypeFilter := strings.TrimSpace(r.URL.Query().Get("type"))

	// Parse optional offset + limit
	offset := 0
	limit := searchDefaultLimit
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= searchMaxLimit {
			limit = n
		}
	}

	results := SearchResults{
		Projects: []SearchProject{},
		Issues:   []SearchIssue{},
		Users:    []SearchUser{},
		Tags:     []models.Tag{},
		HasMore:  false,
	}

	scopedProjectID, ok := resolveSearchScopedProjectID(r)
	if !ok {
		jsonOK(w, results)
		return
	}

	if len(q) < 2 {
		jsonOK(w, results)
		return
	}

	dedup := &issueDedup{}

	// The lookupIssueByKey / lookupIssuesByProjKey / lookupIssuesByKeyPrefix
	// helpers don't apply the accessible-project filter themselves, so any
	// early return from the key-lookup paths below must scrub unauthorized
	// hits before responding. filterDedup drops issues whose project is not
	// in the caller's accessible set; nil means "admin, no filter".
	accessibleIDs := auth.AccessibleProjectIDs(r)
	filterDedup := func() {
		if len(dedup.list) == 0 {
			return
		}
		allowed := map[int64]bool(nil)
		if accessibleIDs != nil {
			allowed = make(map[int64]bool, len(accessibleIDs))
			for _, pid := range accessibleIDs {
				allowed[pid] = true
			}
		}
		filtered := dedup.list[:0]
		for _, iss := range dedup.list {
			if user := auth.GetUser(r); user != nil && user.Role == auth.RoleReviewer && iss.ProjectID == nil {
				continue
			}
			if allowed != nil && iss.ProjectID != nil && !allowed[*iss.ProjectID] {
				continue
			}
			if scopedProjectID > 0 && (iss.ProjectID == nil || *iss.ProjectID != scopedProjectID) {
				continue
			}
			if !matchesSearchIssueType(iss.Type, issueTypeFilter) {
				continue
			}
			filtered = append(filtered, iss)
		}
		dedup.list = filtered
	}

	// ── Issue key lookup (bypasses FTS — keys are computed, not indexed) ──────
	qUpper := strings.ToUpper(q)

	if m := keyPattern.FindStringSubmatch(qUpper); m != nil {
		num, _ := strconv.Atoi(m[2])
		// Exact match first
		if iss, ok := lookupIssueByKey(m[1], num); ok {
			dedup.add(iss)
		}
		// Partial key prefix matches (e.g. "ACME-1" also finds ACME-1, ACME-1)
		dedup.addAll(lookupIssuesByKeyPrefix(m[1], m[2]))
		filterDedup()
		if len(dedup.list) > 0 {
			results.Issues, results.HasMore = dedup.result(offset, limit)
			jsonOK(w, results)
			return
		}
	}

	if m := keyPrefixPattern.FindStringSubmatch(qUpper); m != nil {
		parts := strings.SplitN(m[1], "-", 2)
		if len(parts) == 2 {
			num, _ := strconv.Atoi(parts[1])
			if iss, ok := lookupIssueByKey(parts[0], num); ok {
				dedup.add(iss)
			}
		}
		q = strings.TrimSpace(m[2])
		qUpper = strings.ToUpper(q)
	}

	if m := projKeyPattern.FindStringSubmatch(qUpper); m != nil {
		if issues := lookupIssuesByProjKey(m[1]); len(issues) > 0 {
			dedup.addAll(issues)
			filterDedup()
			if len(dedup.list) > 0 {
				results.Issues, results.HasMore = dedup.result(offset, limit)
				jsonOK(w, results)
				return
			}
		}
	}

	// FTS5 prefix query — sanitize to prevent parser crashes from
	// special characters (e.g. `doc/` → `fts5: syntax error near "/"`).
	// PAI-283 phase 2.
	ftsQuery, useFTS := sanitizeFTS5Token(q)
	if !useFTS {
		filterDedup()
		// Input had no tokenizable content (only special characters).
		// All FTS5 paths below would either crash the parser or match
		// nothing; the LIKE-based key path (line ~484) needs a token
		// too. Return what we have so far (issue/proj-key lookups
		// already ran above) without running the FTS5 fan-out.
		results.Issues, results.HasMore = dedup.result(offset, limit)
		jsonOK(w, results)
		return
	}

	// Per-user access filter applied to project- and issue-returning
	// queries below. For admins `accessFilter` is empty and no args
	// are added; for restricted users it narrows results.
	accessProjectFilter, accessProjectArgs := projectIDFilter(r, "p.id", false)
	accessIssueFilter, accessIssueArgs := projectIDFilter(r, "i.project_id", true)
	scopeProjectFilter := ""
	scopeProjectArgs := []any{}
	scopeIssueFilter := ""
	scopeIssueArgs := []any{}
	if scopedProjectID > 0 {
		scopeProjectFilter = " AND p.id = ?"
		scopeProjectArgs = append(scopeProjectArgs, scopedProjectID)
		scopeIssueFilter = " AND i.project_id = ?"
		scopeIssueArgs = append(scopeIssueArgs, scopedProjectID)
	}
	typeIssueFilter := ""
	typeIssueArgs := []any{}
	if issueTypeFilter != "" {
		typeIssueFilter = " AND LOWER(i.type) = LOWER(?)"
		typeIssueArgs = append(typeIssueArgs, issueTypeFilter)
	}

	// ── Projects ─────────────────────────────────────────────────────────────
	projArgs := append([]any{ftsQuery}, accessProjectArgs...)
	projArgs = append(projArgs, scopeProjectArgs...)
	// #nosec G202 G701 -- SQL is fixed-fragment assembly; all user values are placeholders.
	projRows, err := db.DB.Query(`
		SELECT p.id, p.name, p.key, p.description, p.status
		FROM search_index si
		JOIN projects p ON p.id = si.entity_id
		WHERE si.entity_type = 'project'
		  AND search_index MATCH ?`+accessProjectFilter+scopeProjectFilter+`
		LIMIT 5
	`, projArgs...)
	if err == nil {
		defer projRows.Close()
		for projRows.Next() {
			var p SearchProject
			if err := projRows.Scan(&p.ID, &p.Name, &p.Key, &p.Description, &p.Status); err == nil {
				p.Tags = []models.Tag{}
				results.Projects = append(results.Projects, p)
			}
		}
	}

	// ── Issues (FTS content match) ────────────────────────────────────────────
	issArgs := append([]any{ftsQuery}, accessIssueArgs...)
	issArgs = append(issArgs, scopeIssueArgs...)
	issArgs = append(issArgs, typeIssueArgs...)
	issArgs = append(issArgs, limit+1)
	// #nosec G202 G701 -- SQL is fixed-fragment assembly; all user values are placeholders.
	issRows, err := db.DB.Query(`
		SELECT `+issueSelectCols+`
		FROM search_index si
		JOIN issues i ON i.id = si.entity_id
		LEFT JOIN projects p ON p.id = i.project_id
		LEFT JOIN users u ON u.id = i.assignee_id
		WHERE si.entity_type = 'issue'
		  AND search_index MATCH ?
		  AND i.deleted_at IS NULL`+accessIssueFilter+scopeIssueFilter+typeIssueFilter+`
		LIMIT ?
	`, issArgs...)
	if err == nil {
		dedup.addAll(scanIssueRows(issRows))
	}

	// Instance directories and the global tag vocabulary are not shared-project
	// data. Reviewers search projects/issues only; their tag picker is scoped.
	if user := auth.GetUser(r); user == nil || user.Role != auth.RoleReviewer {
		// ── Users ─────────────────────────────────────────────────────────────────
		userRows, err := db.DB.Query(`
		SELECT u.id, u.username,
		       CASE
		         WHEN u.is_reviewer = 1 THEN 'reviewer'
		         WHEN u.is_super_admin = 1 THEN 'super_admin'
		         WHEN u.role_key = 'member' AND u.role IN ('admin','external') THEN u.role
		         WHEN u.role_key IN ('admin','member','external','super_admin') THEN u.role_key
		         WHEN u.role IN ('admin','member','external') THEN u.role
		         ELSE 'member'
		       END
		FROM search_index si
		JOIN users u ON u.id = si.entity_id
		WHERE si.entity_type = 'user'
		  AND search_index MATCH ?
		LIMIT 5
	`, ftsQuery)
		if err == nil {
			defer userRows.Close()
			for userRows.Next() {
				var u SearchUser
				if err := userRows.Scan(&u.ID, &u.Username, &u.Role); err == nil {
					results.Users = append(results.Users, u)
				}
			}
		}

		// ── Tags ──────────────────────────────────────────────────────────────────
		tagRows, err := db.DB.Query(`
		SELECT t.id, t.name, t.color, t.description, t.system, t.created_at
		FROM search_index si
		JOIN tags t ON t.id = si.entity_id
		WHERE si.entity_type = 'tag'
		  AND search_index MATCH ?
		LIMIT 5
	`, ftsQuery)
		if err == nil {
			defer tagRows.Close()
			for tagRows.Next() {
				var t models.Tag
				if err := tagRows.Scan(&t.ID, &t.Name, &t.Color, &t.Description, &t.System, &t.CreatedAt); err == nil {
					results.Tags = append(results.Tags, t)
				}
			}
		}

	}
	// ── Issues via tag match ──────────────────────────────────────────────────
	if len(results.Tags) > 0 {
		tagIDs := make([]any, len(results.Tags))
		for i, t := range results.Tags {
			tagIDs[i] = t.ID
		}
		ph := buildPlaceholders(len(tagIDs))

		// Projects with matching tags
		tpArgs := append([]any{}, tagIDs...)
		tpArgs = append(tpArgs, accessProjectArgs...)
		tpArgs = append(tpArgs, scopeProjectArgs...)
		// #nosec G202 G701 -- placeholders and filters are fixed-fragment assembly.
		tpRows, err := db.DB.Query(`
			SELECT DISTINCT p.id, p.name, p.key, p.description, p.status
			FROM project_tags pt
			JOIN projects p ON p.id = pt.project_id
			WHERE pt.tag_id IN (`+ph+`)`+accessProjectFilter+scopeProjectFilter+`
			LIMIT 5
		`, tpArgs...)
		if err == nil {
			defer tpRows.Close()
			for tpRows.Next() {
				var p SearchProject
				if err := tpRows.Scan(&p.ID, &p.Name, &p.Key, &p.Description, &p.Status); err == nil {
					p.Tags = []models.Tag{}
					// Add to projects only if not already present
					found := false
					for _, ep := range results.Projects {
						if ep.ID == p.ID {
							found = true
							break
						}
					}
					if !found {
						results.Projects = append(results.Projects, p)
					}
				}
			}
		}

		tiArgs := append([]any{}, tagIDs...)
		tiArgs = append(tiArgs, accessIssueArgs...)
		tiArgs = append(tiArgs, scopeIssueArgs...)
		tiArgs = append(tiArgs, typeIssueArgs...)
		tiArgs = append(tiArgs, limit+1)
		// #nosec G202 G701 -- placeholders and filters are fixed-fragment assembly.
		tiRows, err := db.DB.Query(`
			SELECT DISTINCT `+issueSelectCols+`
			FROM issue_tags it
			JOIN issues i ON i.id = it.issue_id
			LEFT JOIN projects p ON p.id = i.project_id
			LEFT JOIN users u ON u.id = i.assignee_id
			WHERE it.tag_id IN (`+ph+`) AND i.deleted_at IS NULL`+accessIssueFilter+scopeIssueFilter+typeIssueFilter+`
			LIMIT ?
		`, tiArgs...)
		if err == nil {
			dedup.addAll(scanIssueRows(tiRows))
		}
	}

	// ── Issues via assignee match ─────────────────────────────────────────────
	if len(results.Users) > 0 {
		userIDs := make([]any, len(results.Users))
		for i, u := range results.Users {
			userIDs[i] = u.ID
		}
		ph := buildPlaceholders(len(userIDs))

		aiArgs := append([]any{}, userIDs...)
		aiArgs = append(aiArgs, accessIssueArgs...)
		aiArgs = append(aiArgs, scopeIssueArgs...)
		aiArgs = append(aiArgs, typeIssueArgs...)
		aiArgs = append(aiArgs, limit+1)
		// #nosec G202 G701 -- placeholders and filters are fixed-fragment assembly.
		aiRows, err := db.DB.Query(`
			SELECT `+issueSelectCols+`
			FROM issues i
			LEFT JOIN projects p ON p.id = i.project_id
			LEFT JOIN users u ON u.id = i.assignee_id
			WHERE i.assignee_id IN (`+ph+`) AND i.deleted_at IS NULL`+accessIssueFilter+scopeIssueFilter+typeIssueFilter+`
			ORDER BY i.updated_at DESC
			LIMIT ?
		`, aiArgs...)
		if err == nil {
			dedup.addAll(scanIssueRows(aiRows))
		}
	}

	// ── Issues via issue_key LIKE match ──────────────────────────────────────
	keyArgs := []any{"%" + q + "%"}
	keyArgs = append(keyArgs, accessIssueArgs...)
	keyArgs = append(keyArgs, scopeIssueArgs...)
	keyArgs = append(keyArgs, typeIssueArgs...)
	keyArgs = append(keyArgs, limit+1)
	// #nosec G202 G701 -- SQL is fixed-fragment assembly; all user values are placeholders.
	keyLikeRows, err := db.DB.Query(`
		SELECT `+issueSelectCols+`
		FROM issues i
		LEFT JOIN projects p ON p.id = i.project_id
		LEFT JOIN users u ON u.id = i.assignee_id
		WHERE (COALESCE(p.key,'') || '-' || CAST(i.issue_number AS TEXT)) LIKE ? AND i.deleted_at IS NULL`+accessIssueFilter+scopeIssueFilter+typeIssueFilter+`
		ORDER BY i.updated_at DESC
		LIMIT ?
	`, keyArgs...)
	if err == nil {
		dedup.addAll(scanIssueRows(keyLikeRows))
	}

	// ── Issues via comment match ──────────────────────────────────────────────
	// Comments are indexed as entity_type='comment'; we join back to the parent issue.
	cmtArgs := []any{ftsQuery}
	cmtArgs = append(cmtArgs, accessIssueArgs...)
	cmtArgs = append(cmtArgs, scopeIssueArgs...)
	cmtArgs = append(cmtArgs, typeIssueArgs...)
	cmtArgs = append(cmtArgs, limit+1)
	// #nosec G202 G701 -- SQL is fixed-fragment assembly; all user values are placeholders.
	commentIssRows, err := db.DB.Query(`
		SELECT DISTINCT `+issueSelectCols+`
		FROM search_index si
		JOIN comments c ON c.id = si.entity_id
		JOIN issues i ON i.id = c.issue_id
		LEFT JOIN projects p ON p.id = i.project_id
		LEFT JOIN users u ON u.id = i.assignee_id
		WHERE si.entity_type = 'comment'
		  AND search_index MATCH ?
		  AND i.deleted_at IS NULL`+accessIssueFilter+scopeIssueFilter+typeIssueFilter+`
		LIMIT ?
	`, cmtArgs...)
	if err == nil {
		dedup.addAll(scanIssueRows(commentIssRows))
	}

	// Defence-in-depth: the key-lookup paths above (lookupIssueByKey,
	// lookupIssuesByProjKey, lookupIssuesByKeyPrefix) don't apply the
	// access filter, so drop any issues the caller can't view before
	// paginating.
	if len(dedup.list) > 0 {
		filtered := dedup.list[:0]
		for _, iss := range dedup.list {
			if iss.ProjectID != nil && !auth.CanViewProject(r, *iss.ProjectID) {
				continue
			}
			if scopedProjectID > 0 && (iss.ProjectID == nil || *iss.ProjectID != scopedProjectID) {
				continue
			}
			if !matchesSearchIssueType(iss.Type, issueTypeFilter) {
				continue
			}
			filtered = append(filtered, iss)
		}
		dedup.list = filtered
	}
	if len(results.Projects) > 0 {
		filtered := results.Projects[:0]
		for _, p := range results.Projects {
			if !auth.CanViewProject(r, p.ID) {
				continue
			}
			if scopedProjectID > 0 && p.ID != scopedProjectID {
				continue
			}
			filtered = append(filtered, p)
		}
		results.Projects = filtered
	}

	results.Issues, results.HasMore = dedup.result(offset, limit)
	jsonOK(w, results)
}

func resolveSearchScopedProjectID(r *http.Request) (int64, bool) {
	if ref := strings.TrimSpace(r.URL.Query().Get("project")); ref != "" {
		return resolveSearchProjectRef(r, ref)
	}
	if !strings.EqualFold(r.URL.Query().Get("scope"), "project") {
		return 0, true
	}
	return resolveSearchProjectRef(r, r.URL.Query().Get("project_id"))
}

func resolveSearchProjectRef(r *http.Request, ref string) (int64, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, false
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if id <= 0 || !auth.CanViewProject(r, id) {
			return 0, false
		}
		return id, true
	}

	var id int64
	if err := db.DB.QueryRow(`
		SELECT id
		FROM projects
		WHERE UPPER(key) = UPPER(?)
		LIMIT 1
	`, ref).Scan(&id); err != nil {
		return 0, false
	}
	if id <= 0 || !auth.CanViewProject(r, id) {
		return 0, false
	}
	return id, true
}

func matchesSearchIssueType(issueType, filter string) bool {
	return strings.TrimSpace(filter) == "" || strings.EqualFold(issueType, filter)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
