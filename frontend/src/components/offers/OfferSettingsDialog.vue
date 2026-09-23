<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { api, errMsg } from '@/api/client'
import type { OfferSettings, OfferSender } from './types'
import defaults from './prototype-defaults.json'
const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: []; saved: [settings: OfferSettings] }>()
const settings = ref<OfferSettings>()
const error = ref('')
const saving = ref(false)
const tab = ref<'sender' | 'texts'>('sender')
const form = ref<HTMLFormElement>()
const dialog = ref<HTMLDialogElement>()
const senderFields: { key: keyof OfferSender; label: string; required?: boolean }[] = [
  { key: 'company', label: 'Firma', required: true },
  { key: 'street', label: 'Straße', required: true },
  { key: 'postal_code', label: 'PLZ', required: true },
  { key: 'city', label: 'Ort', required: true },
  { key: 'country', label: 'Land', required: true },
  { key: 'email', label: 'E-Mail', required: true },
  { key: 'contact_person', label: 'Ansprechpartner' },
  { key: 'register_no', label: 'Firmenbuchnummer' },
  { key: 'register_court', label: 'Firmenbuchgericht' },
  { key: 'uid', label: 'UID (optional)' },
  { key: 'phone', label: 'Telefon' },
  { key: 'website', label: 'Website' },
  { key: 'bank_name', label: 'Bank' },
  { key: 'iban', label: 'IBAN (optional)' },
  { key: 'bic', label: 'BIC (optional)' },
]
watch(
  () => props.open,
  async (open) => {
    if (!open) { dialog.value?.close(); return }
    await nextTick()
    if (dialog.value && !dialog.value.open) dialog.value.showModal()
    error.value = ''
    try {
      settings.value = await api.get<OfferSettings>('/integrations/crm/offers')
      if (!settings.value.defaults.blocks.length && !settings.value.defaults.intro && !settings.value.defaults.accept_text)
        settings.value.defaults = structuredClone(defaults)
      await nextTick()
      form.value?.querySelector<HTMLElement>('button')?.focus()
    } catch (e) {
      error.value = errMsg(e)
    }
  },
  { immediate: true },
)
async function save() {
  if (!settings.value) return
  saving.value = true
  error.value = ''
  try {
    const saved = await api.put<OfferSettings>('/integrations/crm/offers', settings.value)
    emit('saved', saved)
    emit('close')
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
  }
}
</script>
<template>
  <dialog ref="dialog" class="offer-settings-dialog" aria-label="Angebots-Einstellungen" @cancel.prevent="emit('close')"><header><h2>Angebots-Einstellungen</h2><button type="button" aria-label="Schließen" @click="emit('close')">×</button></header><form
      novalidate
      ref="form"
      class="offer-settings"
      @submit.prevent="save"
    >
      <div class="settings-tabs">
        <button type="button" class="btn" :aria-pressed="tab === 'sender'" @click="tab = 'sender'">
          Absender</button
        ><button type="button" class="btn" :aria-pressed="tab === 'texts'" @click="tab = 'texts'">
          Textbausteine
        </button>
      </div>
      <p v-if="error" role="alert">{{ error }}</p>
      <template v-if="settings"
        ><div v-show="tab === 'sender'" class="sender-fields">
          <label v-for="field in senderFields" :key="field.key"
            >{{ field.label
            }}<input
              v-model="settings.sender[field.key]"
              :required="field.required"
              :type="field.key === 'email' ? 'email' : 'text'"
          /></label>
        </div>
        <div v-show="tab === 'texts'" class="text-fields">
          <p>
            Textvorlagen gelten für neue Angebote. Der Absender wird auch in einen geöffneten
            Entwurf übernommen. Finalisierte Angebote bleiben unverändert. Die Vorschläge stammen
            aus deiner Angebotsvorlage und sind frei editierbar.
          </p>
          <label>Einleitung<textarea v-model="settings.defaults.intro" rows="3" /></label>
          <div v-for="(block, i) in settings.defaults.blocks" :key="i">
            <label>Abschnitt {{ i + 1 }}<input v-model="block.heading" /></label
            ><textarea
              v-model="block.body"
              rows="4"
              :readonly="!!block.nodes?.length"
              :aria-label="`Text Abschnitt ${i + 1}`"
            /><p v-if="block.nodes?.length">
              Dieser Baustein enthält eine Aufzählung. Sie wird im Angebot bearbeitet und bleibt beim
              Speichern erhalten.
            </p><button
              type="button"
              class="btn btn-sm"
              @click="settings.defaults.blocks.splice(i, 1)"
            >
              Abschnitt entfernen
            </button>
          </div>
          <button
            type="button"
            class="btn"
            :disabled="settings.defaults.blocks.length >= 20"
            @click="settings.defaults.blocks.push({ heading: '', body: '' })"
          >
            + Textbaustein</button
          ><label>Annahmetext<textarea v-model="settings.defaults.accept_text" rows="3" /></label
          ><label>Umsatzsteuerhinweis<input v-model="settings.defaults.vat_note" /></label>
        </div>
        <div class="settings-actions">
          <button type="button" class="btn" @click="emit('close')">Abbrechen</button
          ><button class="btn btn-primary" :disabled="saving">
            {{ saving ? 'Speichert …' : 'Einstellungen speichern' }}
          </button>
        </div></template
      >
      <p v-else>Lädt …</p>
    </form></dialog>
</template>
<style scoped>
.offer-settings-dialog { --bg-card:#fffefa; --bg:#f7f6f2; --text:#203c3d; --text-muted:#596e70; --border:#dfe6e5; color:var(--text); background:var(--bg-card); border:1px solid var(--border); border-radius:12px; padding:20px 24px; width:min(680px,calc(100vw - 32px)); max-height:85vh; overflow:auto; box-shadow:0 12px 40px #10232733; }
.offer-settings-dialog::backdrop { background:#10232788; }
.offer-settings-dialog>header { display:flex; align-items:center; justify-content:space-between; gap:16px; padding-bottom:14px; }
.offer-settings-dialog h2 { margin:0; font-size:18px; }
.offer-settings-dialog>header button { border:0; background:transparent; color:inherit; font-size:26px; cursor:pointer; }
.settings-actions { position:sticky; bottom:-20px; padding:14px 0; background:var(--bg-card); }

.offer-settings,
.text-fields {
  display: grid;
  gap: 16px;
}
.settings-tabs,
.settings-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}
.sender-fields {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
label {
  display: grid;
  gap: 4px;
  font-size: 13px;
}
input,
textarea {
  width: 100%;
  font: inherit;
  padding: 8px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: var(--bg-card);
  color: var(--text);
}
textarea {
  resize: vertical;
}
.text-fields p {
  font-size: 13px;
  color: var(--text-muted);
}
[role='alert'] {
  color: var(--danger, #b42318);
}
@media (max-width: 540px) {
  .sender-fields {
    grid-template-columns: 1fr;
  }
}
</style>
