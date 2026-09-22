<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { ArrowRight, BookOpen, Command, FileText, Search, Settings2, X } from 'lucide-vue-next'

import type {
  CommandPaletteKnowledgeResult,
  CommandPaletteNodeResult,
  CommandPaletteSearchWire,
  CommandPaletteSessionResult,
  CommandShortcutSource,
} from '@/v6/commandPalette'
import type { CommandPaletteLoadState } from '@/composables/v6/usePaimos6CommandPalette'

export type Paimos6PaletteActivation =
  | { kind: 'session'; row: CommandPaletteSessionResult }
  | { kind: 'node'; row: CommandPaletteNodeResult }
  | { kind: 'knowledge'; row: CommandPaletteKnowledgeResult }
  | { kind: 'action'; action: 'open_talk' | 'clear_session' | 'open_settings' | 'open_crm' | 'return_5x' }

const props = defineProps<{
  open: boolean
  query: string
  search: CommandPaletteSearchWire | null
  searchState: CommandPaletteLoadState
  settingsState: CommandPaletteLoadState
  shortcutLabel: string
  shortcutSource: CommandShortcutSource | null
  selectedSessionId: string | null
  announcement: string
  returnFocus: HTMLElement | null
  // PAI-980: instance CRM switch; adds the Customers door when on.
  crmEnabled?: boolean
  readOnly?: boolean
}>()

const emit = defineEmits<{
  'update:query': [value: string]
  close: []
  activate: [item: Paimos6PaletteActivation]
}>()

interface PaletteItem {
  key: string
  group: string
  label: string
  detail: string
  unavailable?: boolean
  activation: Paimos6PaletteActivation
}

const inputRef = ref<HTMLInputElement | null>(null)
const dialogRef = ref<HTMLElement | null>(null)
const activeIndex = ref(0)
const items = computed<PaletteItem[]>(() => {
  const result: PaletteItem[] = []
  for (const row of props.search?.sessions ?? []) result.push({
    key: `session:${row.product_session_id}`, group: 'Sessions', label: row.title,
    detail: row.summary ?? 'Product session', activation: { kind: 'session', row },
  })
  for (const row of props.search?.nodes ?? []) result.push({
    key: `node:${row.node_id}`, group: 'Nodes', label: `${row.node_key} · ${row.title}`,
    detail: `${row.type_label} · unavailable in this preview`, unavailable: true, activation: { kind: 'node', row },
  })
  for (const row of props.search?.knowledge ?? []) result.push({
    key: `knowledge:${row.knowledge_id}`, group: 'Knowledge', label: row.title,
    detail: `${row.type_label} · ${row.slug}`, activation: { kind: 'knowledge', row },
  })
  const actions: PaletteItem[] = [
    { key: 'action:open_talk', group: 'Shell actions', label: 'Open voice panel', detail: 'Open the voice capture panel', activation: { kind: 'action', action: 'open_talk' } },
    { key: 'action:open_settings', group: 'Shell actions', label: 'Command shortcut settings', detail: 'Open Settings → Account', activation: { kind: 'action', action: 'open_settings' } },
    { key: 'action:return_5x', group: 'Shell actions', label: 'Open 5.x dashboard', detail: 'Navigate to the legacy dashboard at /legacy', activation: { kind: 'action', action: 'return_5x' } },
  ]
  if (props.crmEnabled) actions.splice(actions.length - 1, 0, {
    key: 'action:open_crm', group: 'Shell actions', label: 'Customers',
    detail: 'Open the customer list at /crm', activation: { kind: 'action', action: 'open_crm' },
  })
  if (props.selectedSessionId) actions.splice(1, 0, {
    key: 'action:clear_session', group: 'Shell actions', label: 'Clear selected session',
    detail: 'Clear the current authorized v6 selection', activation: { kind: 'action', action: 'clear_session' },
  })
  return [...result, ...actions.filter(item => !props.readOnly || item.key === 'action:clear_session')]
})
const activeId = computed(() => items.value[activeIndex.value] ? `p6-command-${activeIndex.value}` : undefined)

watch(() => props.open, async (open) => {
  activeIndex.value = 0
  await nextTick()
  if (open) inputRef.value?.focus()
  else props.returnFocus?.focus()
})
watch(items, () => { activeIndex.value = Math.min(activeIndex.value, Math.max(0, items.value.length - 1)) })

function activate(index = activeIndex.value) {
  const item = items.value[index]
  if (item) emit('activate', item.activation)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    emit('close')
  } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    const direction = event.key === 'ArrowDown' ? 1 : -1
    activeIndex.value = (activeIndex.value + direction + items.value.length) % items.value.length
  } else if (event.key === 'Enter') {
    event.preventDefault()
    activate()
  } else if (event.key === 'Tab') {
    const focusable = [...(dialogRef.value?.querySelectorAll<HTMLElement>('button, input, [href], [tabindex]:not([tabindex="-1"])') ?? [])]
    if (focusable.length === 0) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }
}
</script>

<template>
  <div v-if="open" class="p6-command-layer">
    <button class="p6-command-backdrop" type="button" tabindex="-1" aria-label="Close command palette" @click="emit('close')" />
    <section
      ref="dialogRef"
      class="p6-command-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="p6-command-title"
      @keydown="onKeydown"
    >
      <header>
        <div><h2 id="p6-command-title">Command palette</h2></div>
        <button type="button" class="p6-command-close" aria-label="Close command palette" @click="emit('close')"><X :size="18" /></button>
      </header>
      <label class="p6-command-search">
        <Search :size="17" aria-hidden="true" />
        <input
          ref="inputRef"
          :value="query"
          role="combobox"
          aria-label="Search authorized sessions, nodes, and knowledge"
          aria-controls="p6-command-results"
          aria-autocomplete="list"
          aria-expanded="true"
          :aria-activedescendant="activeId"
          autocomplete="off"
          placeholder="Search this authorized project"
          @input="emit('update:query', ($event.target as HTMLInputElement).value)"
        />
        <kbd>{{ shortcutLabel }}</kbd>
      </label>
      <p class="p6-command-setting">
        <Command :size="13" aria-hidden="true" /> Effective shortcut · {{ shortcutLabel }} · {{ shortcutSource ?? settingsState }}
        <span>Override and reset controls are in Settings → Account.</span>
      </p>

      <div id="p6-command-results" class="p6-command-results" role="listbox" aria-label="Command results">
        <p v-if="searchState === 'loading'" class="p6-command-state" role="status">Searching authorized project…</p>
        <p v-else-if="searchState === 'empty'" class="p6-command-state">No matching sessions, nodes, or knowledge.</p>
        <p v-else-if="searchState === 'unavailable'" class="p6-command-state is-unavailable" role="alert">Project search unavailable. No prior results are shown.</p>
        <template v-for="(item, index) in items" :key="item.key">
          <h3 v-if="index === 0 || items[index - 1]?.group !== item.group">{{ item.group }}</h3>
          <button
            :id="`p6-command-${index}`"
            type="button"
            role="option"
            :aria-selected="activeIndex === index"
            :aria-disabled="item.unavailable || undefined"
            :class="{ 'is-active': activeIndex === index, 'is-unavailable': item.unavailable }"
            @mouseenter="activeIndex = index"
            @click="activate(index)"
          >
            <span class="p6-command-item-icon" aria-hidden="true">
              <FileText v-if="item.activation.kind === 'session'" :size="16" />
              <BookOpen v-else-if="item.activation.kind === 'knowledge'" :size="16" />
              <Settings2 v-else :size="16" />
            </span>
            <span><strong>{{ item.label }}</strong><small>{{ item.detail }}</small></span>
            <ArrowRight :size="15" aria-hidden="true" />
          </button>
        </template>
      </div>
      <p class="p6-command-announcement" role="status" aria-live="polite">{{ announcement }}</p>
    </section>
  </div>
</template>

<style scoped>
.p6-command-layer { position: fixed; inset: 0; z-index: 30; display: grid; place-items: start center; padding: 9vh 18px 18px; }
.p6-command-backdrop { position: absolute; inset: 0; width: 100%; height: 100%; border: 0; background: rgba(22, 31, 27, .42); }
.p6-command-dialog { position: relative; width: min(680px, 100%); max-height: 82vh; overflow: hidden; border: 1px solid #cbd8d0; border-radius: 20px; background: #fbfcfa; box-shadow: 0 28px 90px rgba(20, 40, 30, .24); }
.p6-command-dialog > header { display: flex; align-items: center; justify-content: space-between; padding: 18px 20px 12px; }
.p6-command-dialog h2 { margin: 0; font: 600 18px/1.1 "Bricolage Grotesque", sans-serif; }
.p6-command-close { display: grid; width: 44px; height: 44px; place-items: center; border: 1px solid #d8e1db; border-radius: 11px; background: #fff; }
.p6-command-search { display: flex; min-height: 52px; align-items: center; gap: 9px; margin: 0 20px; padding: 0 13px; border: 1px solid #aac0b3; border-radius: 12px; background: #fff; }
.p6-command-search input { min-width: 0; flex: 1; border: 0; outline: 0; background: transparent; font-size: 14px; }
.p6-command-search kbd { color: #58665f; font: 600 10px/1 "JetBrains Mono", monospace; }
.p6-command-setting { display: flex; flex-wrap: wrap; align-items: center; gap: 5px; padding: 9px 22px; color: #617068; font-size: 9.5px; }
.p6-command-setting span { margin-left: auto; }
.p6-command-results { max-height: 54vh; overflow-y: auto; padding: 4px 10px 14px; }
.p6-command-results h3 { padding: 10px 11px 5px; color: #68756e; font-size: 9px; letter-spacing: .08em; text-transform: uppercase; }
.p6-command-results button { display: grid; width: 100%; min-height: 52px; grid-template-columns: auto 1fr auto; align-items: center; gap: 10px; padding: 8px 11px; border: 0; border-radius: 10px; color: #26352d; background: transparent; text-align: left; }
.p6-command-results button.is-active { background: #eaf3ed; }
.p6-command-results button.is-unavailable { color: #66736c; }
.p6-command-results strong, .p6-command-results small { display: block; }
.p6-command-results strong { font-size: 12px; }
.p6-command-results small { margin-top: 2px; color: #68756e; font-size: 10px; }
.p6-command-item-icon { display: grid; width: 32px; height: 32px; place-items: center; border-radius: 9px; background: #f0f4f1; }
.p6-command-state { margin: 8px; padding: 16px; border: 1px dashed #ccd8d1; border-radius: 10px; color: #5d6c64; font-size: 11px; text-align: center; }
.p6-command-state.is-unavailable { border-color: #dfc7bd; color: #784d3b; background: #fff8f4; }
.p6-command-announcement { min-height: 25px; padding: 4px 20px 12px; color: #5c6a62; font-size: 10px; }
button:focus-visible, input:focus-visible { outline: 3px solid rgba(47, 107, 82, .3); outline-offset: 2px; }
@media (max-width: 680px) {
  .p6-command-layer { align-items: end; padding: 8px; }
  .p6-command-dialog { width: 100%; max-height: calc(100vh - 16px); border-radius: 18px; }
  .p6-command-results { max-height: 52vh; }
  .p6-command-setting span { width: 100%; margin-left: 0; }
  .p6-command-results button { min-height: 52px; }
}
</style>
