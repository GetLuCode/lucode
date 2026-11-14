import type { Agent } from '../types'

// The Supervisor agent is the global agent named "Supervisor" (internal/db IsSupervisor). It is
// not a worker: it configures the Supervisor panel, is never assigned a task, and can only be
// chosen as the "reports to" of other agents.
export const isSupervisorAgent = (a: Pick<Agent, 'name' | 'scope'>) =>
  a.scope === 'global' && a.name.trim().toLowerCase() === 'supervisor'

export const assignableAgents = (agents: Agent[]) => agents.filter(a => !isSupervisorAgent(a))
