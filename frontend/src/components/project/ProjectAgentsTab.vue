<script setup lang="ts">
// PAI-504 — Agents primary tab. Thin wrapper that mounts the existing
// ProjectAgentsSection (which loads its agent list on mount and keeps
// the inline create / edit / delete UX + canWrite gating intact) and
// re-emits its @count so ProjectDetailView can keep the footer-bar
// badge fresh. Mirrors the Knowledge tab's wrapper shape
// (pku-root → pat-root): a vertical flex column with the same gutters.
//
// Reads are allowed for project viewers; writes require admin + edit
// rights, which the parent passes via `can-write`. This wrapper is
// deliberately empty of logic — the section component owns everything.

import ProjectAgentsSection from '@/components/project/ProjectAgentsSection.vue'

defineProps<{
  projectId: number
  canWrite: boolean
  reviewer?: boolean
}>()

defineEmits<{
  count: [n: number]
}>()
</script>

<template>
  <div class="pat-root">
    <ProjectAgentsSection
      :project-id="projectId"
      :can-write="canWrite"
      :reviewer="reviewer"
      @count="(n: number) => $emit('count', n)"
    />
  </div>
</template>

<style scoped>
/* Mirror pku-root: vertical flex column with the same content gutters
   the other primary tabs use. ProjectAgentsSection carries its own
   internal spacing, so this just establishes the column + min-width
   contract for the page-content flex chain. */
.pat-root {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  padding: .25rem 0;
  min-width: 0;
}
</style>
