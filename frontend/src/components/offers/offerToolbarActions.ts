export type OfferToolbarActionId =
  | 'link'
  | 'settings'
  | 'position'
  | 'duplicate'
  | 'finalize'
  | 'layout'
  | 'delete'

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
  linkAvailable: boolean
  deleted?: boolean
  deleting?: boolean
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
  if (input.isAdmin && !input.printMode) {
    actions.push({
      id: 'duplicate',
      label: 'Duplizieren',
      detail: 'Erstellt ein neues bearbeitbares Angebot mit demselben Inhalt.',
      disabled: input.saving,
    })
  }
  if (input.editable) {
    actions.push({
      id: 'finalize',
      label: 'Finalisieren',
      detail:
        'Schreibt das Angebot fest und erzeugt den Kundenlink. Es wird keine E-Mail gesendet.',
      disabled: input.saving || input.overflow,
    })
  }
  if (input.isAdmin && !input.printMode) {
    actions.push({
      id: 'delete',
      label: input.deleted
        ? 'Angebot wiederherstellen'
        : input.status === 'draft'
          ? 'Angebot als gelöscht markieren'
          : 'Angebot archivieren',
      detail: input.deleted
        ? 'Blendet das Angebot wieder in den Übersichten ein.'
        : 'Blendet das Angebot aus den Übersichten aus. Inhalte, Nachweise und Kundenlinks bleiben.',
      disabled: !!input.deleting,
    })
  }
  return actions
}
