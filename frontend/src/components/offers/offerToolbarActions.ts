export type OfferToolbarActionId =
  | 'link'
  | 'settings'
  | 'position'
  | 'duplicate'
  | 'finalize'
  | 'layout'
  | 'chrome'

export type OfferToolbarAction = {
  id: OfferToolbarActionId
  label: string
  detail: string
  disabled: boolean
}

export function offerToolbarActions(input: {
  hasOffer: boolean
  status: string
  printMode: boolean
  isAdmin: boolean
  editable: boolean
  saving: boolean
  overflow: boolean
  copied: boolean
  collapsed: boolean
  linkAvailable: boolean
}): OfferToolbarAction[] {
  if (!input.hasOffer) return []
  const actions: OfferToolbarAction[] = []
  if (input.status !== 'draft' && !input.printMode && input.linkAvailable) {
    actions.push({
      id: 'link',
      label: input.copied ? 'Link kopiert' : 'Kundenlink kopieren',
      detail:
        'Kopiert den Link. Wer ihn besitzt, kann das Angebot ansehen und bis zum Ablauf annehmen.',
      disabled: false,
    })
  }
  if (input.editable) {
    actions.push(
      {
        id: 'settings',
        label: 'Absender und Textbausteine',
        detail:
          'Speichert Absender und Textvorlagen für neue Angebote. Das geöffnete Angebot bleibt unverändert.',
        disabled: false,
      },
      {
        id: 'position',
        label: 'Position hinzufügen',
        detail: 'Fügt eine Zeile in der Leistungsaufstellung hinzu.',
        disabled: false,
      },
    )
  }
  if (input.isAdmin && !input.printMode) {
    actions.push({
      id: 'duplicate',
      label: 'Duplizieren',
      detail: 'Erstellt ein neues bearbeitbares Angebot mit demselben Inhalt.',
      disabled: input.saving,
    })
  }
  if (input.editable) {
    actions.push(
      {
        id: 'finalize',
        label: 'Finalisieren',
        detail: 'Schreibt das Angebot fest und erzeugt Kundenlink und QR-Code.',
        disabled: input.saving || input.overflow,
      },
      {
        id: 'layout',
        label: 'Fußzeilenlogo',
        detail:
          'Breite und Versatz der Mitte in Millimetern. Negativ hebt, positiv senkt. Nummer, Linie und Seitenzahl bleiben stehen.',
        disabled: false,
      },
    )
  }
  if (!input.printMode) {
    actions.push({
      id: 'chrome',
      label: input.collapsed ? 'Kopfzeilen ausklappen' : 'Kopfzeilen einklappen',
      detail: 'Blendet die Anwendungskopfzeilen ein oder aus.',
      disabled: false,
    })
  }
  return actions
}
