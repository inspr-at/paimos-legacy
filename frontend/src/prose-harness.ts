import { createApp, defineComponent, h, ref } from 'vue'
import OfferProse from './components/offers/OfferProse.vue'
import type { OfferTextNode } from './components/offers/types'
import './components/offers/offer-document.css'

const saved = ref<{ body: string; nodes?: OfferTextNode[] }>({
  body: 'Hello world\nZweite Zeile bleibt ein Absatz.',
})

const Harness = defineComponent({
  name: 'OfferProseHarness',
  setup() {
    return () =>
      h(
        'main',
        {
          class: 'offer-document',
          style: 'max-width: 720px; margin: 32px auto; background: #fff; padding: 24px',
        },
        [
          h('h1', { style: 'font: 600 18px Manrope, sans-serif' }, 'Textbaustein'),
          h('div', { class: 'sec', style: 'margin-top: 28px' }, [
            h('span', { class: 'n' }, '1'),
            h('h3', 'Leistung'),
            h(OfferProse, {
              body: saved.value.body,
              nodes: saved.value.nodes,
              editable: true,
              label: 'Textbaustein 1',
              onUpdate: (value: { body: string; nodes?: OfferTextNode[] }) => {
                saved.value = value
              },
            }),
          ]),
          h(
            'pre',
            { 'data-harness-json': 'true', style: 'white-space: pre-wrap; font-size: 12px' },
            JSON.stringify(saved.value, null, 2),
          ),
        ],
      )
  },
})

createApp(Harness).mount('#app')
