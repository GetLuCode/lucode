// What the status of a logged tool call says, from who decided it and how.
export function toolDecisionLabel(decision?: string, decidedBy?: string): string {
  if (decision === 'auto') return 'Claude auto mode'
  const verb = decision === 'deny' ? 'Denied' : 'Allowed'
  switch (decidedBy) {
    case 'rule': return `${verb} by a rule`
    case 'flow': return `${verb} by a flow`
    default: return verb
  }
}
