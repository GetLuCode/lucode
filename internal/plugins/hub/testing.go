package hub

import "claude-orchestrator/internal/plugins"

// CallSubjectForTest is where this hub takes lucode.server.call (tests).
func (h *Host) CallSubjectForTest() string { return h.remote.call }

// AllowRPCForTest offers an rpc to the browser, as a view tree would (tests).
func AllowRPCForTest(host plugins.HubHost, pluginID, name string) {
	h, ok := host.(*Host)
	if !ok {
		return
	}
	l, err := h.get(pluginID)
	if err != nil {
		return
	}
	l.allowMu.Lock()
	defer l.allowMu.Unlock()
	l.allowed[name] = true
}
