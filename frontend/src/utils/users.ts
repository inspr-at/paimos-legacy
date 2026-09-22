/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 */

export interface AssignableIssueUserCandidate {
  role: string
  status: string
}

export function isAssignableIssueUser(user: AssignableIssueUserCandidate): boolean {
  return user.status === 'active' && user.role !== 'external' && user.role !== 'reviewer'
}

export function assignableIssueUsers<T extends AssignableIssueUserCandidate>(users: readonly T[]): T[] {
  return users.filter((user) => isAssignableIssueUser(user))
}
