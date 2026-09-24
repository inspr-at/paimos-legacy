<script setup lang="ts">
import OfferText from './OfferText.vue'
import { money, quantity, total, parseAmount, type OfferPosition } from './types'
const props = defineProps<{
  positions: OfferPosition[]
  indices: number[]
  editable?: boolean
  selected?: number | null
}>()
const emit = defineEmits<{
  change: []
  remove: [index: number]
  move: [index: number, direction: number]
}>()
function numberInput(e: Event, index: number, price: boolean) {
  const el = e.target as HTMLInputElement
  const value = parseAmount(el.value)
  if (value === null || value > (price ? 1e7 : 1e6)) {
    el.setCustomValidity(
      'Bitte eine gültige positive Zahl mit höchstens zwei Nachkommastellen eingeben.',
    )
    el.reportValidity()
    return
  }
  el.setCustomValidity('')
  if (price) props.positions[index]!.unit_price_cents = Math.round(value * 100)
  else props.positions[index]!.quantity = value
  emit('change')
}
</script>
<template>
  <table class="offer-table">
    <thead>
      <tr>
        <th>Pos.</th>
        <th>Leistung</th>
        <th class="num">Menge</th>
        <th>Einheit</th>
        <th class="num">Einzelpreis</th>
        <th class="num">Betrag</th>
      </tr>
    </thead>
    <tbody
      v-for="i in indices"
      :key="i"
      :data-position="i"
      :class="{ 'last-position': i === positions.length - 1, 'is-selected': selected === i }"
    >
      <tr class="pos">
        <td class="pos-no">{{ String(i + 1).padStart(2, '0') }}</td>
        <td class="short">
          <OfferText
            v-model="positions[i]!.short_text"
            :editable="editable"
            :label="`Leistung Position ${i + 1}`"
          />
        </td>
        <td class="qty num">
          <input
            v-if="editable"
            :value="quantity(positions[i]!.quantity)"
            :aria-label="`Menge Position ${i + 1}`"
            inputmode="decimal"
            @change="numberInput($event, i, false)"
          /><template v-else>{{ quantity(positions[i]!.quantity) }}</template>
        </td>
        <td class="unit">
          <OfferText
            v-model="positions[i]!.unit"
            :editable="editable"
            :label="`Einheit Position ${i + 1}`"
          />
        </td>
        <td class="price num">
          <input
            v-if="editable"
            :value="money(positions[i]!.unit_price_cents)"
            :aria-label="`Einzelpreis Position ${i + 1}`"
            inputmode="decimal"
            @change="numberInput($event, i, true)"
          /><template v-else>{{ money(positions[i]!.unit_price_cents) }}</template>
        </td>
        <td class="sum num">{{ money(total(positions[i]!)) }}</td>
      </tr>
      <tr class="long">
        <td />
        <td colspan="5">
          <OfferText
            v-model="positions[i]!.long_text"
            :editable="editable"
            :label="`Beschreibung Position ${i + 1}`"
          />
        </td>
      </tr>
    </tbody>
  </table>
</template>
