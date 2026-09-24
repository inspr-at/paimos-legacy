<script setup lang="ts">
import { computed } from 'vue'
import { Bold, Italic, Type } from 'lucide-vue-next'
import { useOfferProseSession } from './offerProseSession'
import type { InlineMarkFlag, InlineMarkName } from './offerProse'

const session = useOfferProseSession()
const marks = computed(() => {
  const revision = session?.revision.value ?? 0
  void revision
  return session?.active.value?.state().marks ?? { bold: false, italic: false }
})

function pressed(flag: InlineMarkFlag | undefined): 'true' | 'false' | 'mixed' {
  if (flag === true) return 'true'
  if (flag === 'mixed') return 'mixed'
  return 'false'
}

function apply(mark: InlineMarkName) {
  session?.active.value?.apply({ type: 'inline', mark })
}
</script>
<template>
  <div class="offer-inline-styles" data-offer-chrome role="group" aria-label="Zeichen">
    <button
      type="button"
      :disabled="!session?.active.value"
      :aria-pressed="marks.bold === false && marks.italic === false ? 'true' : 'false'"
      @mousedown.prevent
      @click="apply('normal')"
    >
      <Type :size="15" aria-hidden="true" />
      Normal
    </button>
    <button
      type="button"
      :disabled="!session?.active.value"
      :aria-pressed="pressed(marks.bold)"
      @mousedown.prevent
      @click="apply('bold')"
    >
      <Bold :size="15" aria-hidden="true" />
      Fett
    </button>
    <button
      type="button"
      :disabled="!session?.active.value"
      :aria-pressed="pressed(marks.italic)"
      @mousedown.prevent
      @click="apply('italic')"
    >
      <Italic :size="15" aria-hidden="true" />
      Kursiv
    </button>
  </div>
</template>
<style scoped>
.offer-inline-styles {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 32px;
}
.offer-inline-styles button {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 32px;
  margin: 0;
  padding: 0 8px;
  border: 1px solid #c9d4d2;
  border-radius: 6px;
  background: #fffefa;
  color: #203c3d;
  font: 600 12px/1 Manrope, 'Helvetica Neue', Helvetica, Arial, sans-serif;
  cursor: pointer;
}
.offer-inline-styles button[aria-pressed='true'] {
  background: #c7efec;
  color: #0e6f6c;
  border-color: #8fd4ce;
}
.offer-inline-styles button[aria-pressed='mixed'] {
  background: #f7f6f2;
  color: #0e6f6c;
  border-style: dashed;
}
.offer-inline-styles button:disabled {
  opacity: 0.45;
  cursor: default;
}
.offer-inline-styles button:focus-visible {
  outline: 1px solid #0e6f6c;
  outline-offset: 1px;
}
</style>
