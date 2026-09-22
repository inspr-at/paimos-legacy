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

import "github.com/inspr-at/paimos/backend/models"

// userSelectCols is the full column list for the users table (bare names, for
// direct "SELECT ... FROM users" queries). Role reads are canonicalized through
// role_key; role/is_super_admin remain compatibility shims.
const userRoleSelectExpr = `CASE
	WHEN is_reviewer = 1 THEN 'reviewer'
	WHEN is_super_admin = 1 THEN 'super_admin'
	WHEN role_key = 'member' AND role IN ('admin','external') THEN role
	WHEN role_key IN ('admin','member','external','super_admin') THEN role_key
	WHEN role IN ('admin','member','external') THEN role
	ELSE 'member'
END`
const userSuperAdminSelectExpr = `CASE WHEN ` + userRoleSelectExpr + ` = 'super_admin' OR is_super_admin = 1 THEN 1 ELSE 0 END`
const userSelectCols = `id, username, ` + userRoleSelectExpr + `, status, created_at, nickname, first_name, last_name, email, avatar_path, markdown_default, monospace_fields, recent_projects_limit, internal_rate_hourly, show_alt_unit_table, show_alt_unit_detail, locale, recent_timers_limit, timezone, preview_hover_delay, issue_auto_refresh_enabled, issue_auto_refresh_interval_seconds, last_login_at, accruals_stats_enabled, accruals_extra_statuses, ` + userSuperAdminSelectExpr + `, search_scope_shortcut, command_palette_shortcut`

// userSelectColsWithTOTP appends totp_enabled — used by admin list/update endpoints.
const userSelectColsWithTOTP = userSelectCols + `, totp_enabled`

// scanUser scans the standard user projection into a User struct.
func scanUser(row interface{ Scan(...any) error }, u *models.User) error {
	return row.Scan(&u.ID, &u.Username, &u.Role, &u.Status, &u.CreatedAt,
		&u.Nickname, &u.FirstName, &u.LastName, &u.Email, &u.AvatarPath,
		&u.MarkdownDefault, &u.MonospaceFields, &u.RecentProjectsLimit,
		&u.InternalRateHourly, &u.ShowAltUnitTable, &u.ShowAltUnitDetail, &u.Locale,
		&u.RecentTimersLimit, &u.Timezone, &u.PreviewHoverDelay,
		&u.IssueAutoRefreshEnabled, &u.IssueAutoRefreshIntervalSeconds, &u.LastLoginAt,
		&u.AccrualsStatsEnabled, &u.AccrualsExtraStatuses, &u.IsSuperAdmin,
		&u.SearchScopeShortcut, &u.CommandPaletteShortcut)
}

// scanUserWithTOTP scans the projection with totp_enabled into a User struct.
func scanUserWithTOTP(row interface{ Scan(...any) error }, u *models.User) error {
	return row.Scan(&u.ID, &u.Username, &u.Role, &u.Status, &u.CreatedAt,
		&u.Nickname, &u.FirstName, &u.LastName, &u.Email, &u.AvatarPath,
		&u.MarkdownDefault, &u.MonospaceFields, &u.RecentProjectsLimit,
		&u.InternalRateHourly, &u.ShowAltUnitTable, &u.ShowAltUnitDetail, &u.Locale,
		&u.RecentTimersLimit, &u.Timezone, &u.PreviewHoverDelay,
		&u.IssueAutoRefreshEnabled, &u.IssueAutoRefreshIntervalSeconds, &u.LastLoginAt,
		&u.AccrualsStatsEnabled, &u.AccrualsExtraStatuses, &u.IsSuperAdmin,
		&u.SearchScopeShortcut, &u.CommandPaletteShortcut, &u.TotpEnabled)
}
