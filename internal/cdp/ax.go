package cdp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Element is something on the page, described the way assistive technology sees it:
// a role and an accessible name. Ref is what the action tools take.
type Element struct {
	Ref         string   `json:"ref"`
	Role        string   `json:"role"`
	Name        string   `json:"name"`
	Value       string   `json:"value,omitempty"`
	Description string   `json:"description,omitempty"`
	States      []string `json:"states,omitempty"`
	Level       int      `json:"level,omitempty"` // heading level
	URL         string   `json:"url,omitempty"`   // link target
	Tag         string   `json:"tag,omitempty"`   // raw HTML hint, only for interactive elements without a name
	Depth       int      `json:"depth"`
}

type axValue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

func (v *axValue) str() string {
	if v == nil {
		return ""
	}
	return jsonString(v.Value)
}

type axProp struct {
	Name  string  `json:"name"`
	Value axValue `json:"value"`
}

type axNode struct {
	NodeID           string   `json:"nodeId"`
	Ignored          bool     `json:"ignored"`
	Role             *axValue `json:"role"`
	Name             *axValue `json:"name"`
	Description      *axValue `json:"description"`
	Value            *axValue `json:"value"`
	Properties       []axProp `json:"properties"`
	ParentID         string   `json:"parentId"`
	BackendDOMNodeID int      `json:"backendDOMNodeId"`
}

var interactiveRoles = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true, "combobox": true, "checkbox": true,
	"radio": true, "switch": true, "slider": true, "spinbutton": true, "menuitem": true,
	"menuitemcheckbox": true, "menuitemradio": true, "tab": true, "option": true, "listbox": true,
	"treeitem": true, "colorwell": true, "DisclosureTriangle": true, "PopUpButton": true, "TextField": true,
}

// roles that carry no meaning on their own
var plainRoles = map[string]bool{
	"RootWebArea": true, "WebArea": true, "generic": true, "none": true, "presentation": true, "StaticText": true,
	"InlineTextBox": true, "LineBreak": true, "paragraph": true, "document": true, "Section": true, "": true,
}

var structuralRoles = map[string]bool{
	"heading": true, "navigation": true, "main": true, "banner": true, "contentinfo": true,
	"complementary": true, "form": true, "search": true, "region": true, "dialog": true,
	"alertdialog": true, "alert": true, "status": true, "img": true, "table": true, "list": true,
}

func (n *axNode) role() string { return n.Role.str() }

func (n *axNode) prop(name string) (string, bool) {
	for _, p := range n.Properties {
		if p.Name == name {
			return p.Value.str(), true
		}
	}
	return "", false
}

func (n *axNode) interactive() bool {
	r := n.role()
	if interactiveRoles[r] {
		return true
	}
	if plainRoles[r] {
		return false
	}
	f, ok := n.prop("focusable")
	return ok && f == "true" && n.Name.str() != ""
}

func refOf(backend int) string { return "e" + strconv.Itoa(backend) }

func parseRef(ref string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(ref), "e"))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid ref %q: use a ref returned by browser_list_elements or browser_find_element", ref)
	}
	return n, nil
}

func (n *axNode) element() Element {
	e := Element{
		Ref:         refOf(n.BackendDOMNodeID),
		Role:        n.role(),
		Name:        n.Name.str(),
		Value:       n.Value.str(),
		Description: n.Description.str(),
	}
	for _, name := range []string{"disabled", "focused", "required", "readonly", "selected", "pressed", "checked", "expanded", "invalid"} {
		v, ok := n.prop(name)
		if !ok {
			continue
		}
		switch {
		case name == "expanded" && v == "false":
			e.States = append(e.States, "collapsed")
		case v == "true":
			e.States = append(e.States, name)
		case v == "mixed":
			e.States = append(e.States, name+"=mixed")
		}
	}
	if l, ok := n.prop("level"); ok {
		e.Level, _ = strconv.Atoi(l)
	}
	if u, ok := n.prop("url"); ok {
		e.URL = u
	}
	return e
}

func (b *Browser) axLocked() ([]axNode, error) {
	var out struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := b.pageCallT("Accessibility.getFullAXTree", nil, &out, 30e9); err != nil {
		return nil, err
	}
	return out.Nodes, nil
}

func depths(nodes []axNode) map[string]int {
	parent := map[string]string{}
	for _, n := range nodes {
		parent[n.NodeID] = n.ParentID
	}
	memo := map[string]int{}
	var depth func(id string) int
	depth = func(id string) int {
		if d, ok := memo[id]; ok {
			return d
		}
		p := parent[id]
		d := 0
		if p != "" {
			d = depth(p) + 1
		}
		memo[id] = d
		return d
	}
	for _, n := range nodes {
		depth(n.NodeID)
	}
	return memo
}

// ListOptions filter the elements of a page.
type ListOptions struct {
	Roles []string // only these roles (e.g. button, link, textbox)
	Query string   // only elements whose name, value or description contains it
	All   bool     // include headings, landmarks, images, dialogs... not just the interactive ones
	Limit int      // default 150
}

// List returns the elements of the current page in document order, and how many matched in total.
func (b *Browser) List(o ListOptions) ([]Element, int, PageInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	nodes, err := b.axLocked()
	if err != nil {
		return nil, 0, PageInfo{}, err
	}
	if o.Limit <= 0 {
		o.Limit = 150
	}
	roles := map[string]bool{}
	for _, r := range o.Roles {
		roles[strings.ToLower(strings.TrimSpace(r))] = true
	}
	q := strings.ToLower(strings.TrimSpace(o.Query))
	dep := depths(nodes)
	var els []Element
	total := 0
	for i := range nodes {
		n := &nodes[i]
		if n.Ignored || n.BackendDOMNodeID == 0 {
			continue
		}
		r := n.role()
		if len(roles) > 0 {
			if !roles[strings.ToLower(r)] {
				continue
			}
		} else if !n.interactive() && !(o.All && structuralRoles[r]) {
			continue
		}
		e := n.element()
		if q != "" && !strings.Contains(strings.ToLower(e.Name+" "+e.Value+" "+e.Description), q) {
			continue
		}
		total++
		if len(els) < o.Limit {
			e.Depth = dep[n.NodeID]
			els = append(els, e)
		}
	}
	b.tagUnnamedLocked(els)
	return els, total, b.infoLocked(), nil
}

// tagUnnamedLocked adds a raw HTML hint to the first interactive elements that have no accessible
// name, so the caller can fall back to a CSS selector for them.
func (b *Browser) tagUnnamedLocked(els []Element) {
	budget := 25
	for i := range els {
		e := &els[i]
		if budget == 0 {
			return
		}
		if e.Name != "" || !interactiveRoles[e.Role] {
			continue
		}
		budget--
		id, err := parseRef(e.Ref)
		if err != nil {
			continue
		}
		e.Tag = b.tagOfLocked(id)
	}
}

func (b *Browser) tagOfLocked(backend int) string {
	var d struct {
		Node struct {
			NodeName   string   `json:"nodeName"`
			Attributes []string `json:"attributes"`
		} `json:"node"`
	}
	if err := b.pageCall("DOM.describeNode", map[string]any{"backendNodeId": backend}, &d); err != nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<" + strings.ToLower(d.Node.NodeName))
	for i := 0; i+1 < len(d.Node.Attributes); i += 2 {
		switch k := d.Node.Attributes[i]; k {
		case "id", "name", "type", "class", "placeholder", "data-testid", "href":
			v := d.Node.Attributes[i+1]
			if len(v) > 40 {
				v = v[:40] + "…"
			}
			fmt.Fprintf(&sb, " %s=%q", k, v)
		}
	}
	sb.WriteString(">")
	return sb.String()
}

// FindOptions say what to look for. Role/Name/Text use the accessibility tree; Selector is the
// CSS fallback for elements it cannot name.
type FindOptions struct {
	Role     string
	Name     string // accessible name (label, aria-label, button text...)
	Text     string // any text of the element: name, value, description or the text next to it
	Selector string
	Exact    bool // Name must equal, not just contain
	Limit    int  // default 10
}

// Find returns the elements that match, best match first.
func (b *Browser) Find(o FindOptions) ([]Element, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if o.Limit <= 0 {
		o.Limit = 10
	}
	if o.Selector != "" {
		return b.findCSSLocked(o)
	}
	if o.Role == "" && o.Name == "" && o.Text == "" {
		return nil, errors.New("give at least one of role, name, text or selector")
	}
	nodes, err := b.axLocked()
	if err != nil {
		return nil, err
	}
	byID := map[string]*axNode{}
	for i := range nodes {
		byID[nodes[i].NodeID] = &nodes[i]
	}
	lower := strings.ToLower
	wantRole, wantName, wantText := lower(strings.TrimSpace(o.Role)), lower(strings.TrimSpace(o.Name)), lower(strings.TrimSpace(o.Text))

	type hit struct {
		n     *axNode
		score int
		order int
	}
	var hits []hit
	seen := map[int]bool{}
	for i := range nodes {
		n := &nodes[i]
		if n.Ignored {
			continue
		}
		r := n.role()
		if wantRole != "" && lower(r) != wantRole {
			continue
		}
		name := lower(n.Name.str())
		score := 5
		if wantName != "" {
			switch {
			case name == wantName:
				score = 0
			case o.Exact:
				continue
			case strings.HasPrefix(name, wantName):
				score = 1
			case strings.Contains(name, wantName):
				score = 2
			default:
				continue
			}
		}
		target := n
		if wantText != "" {
			all := name + " " + lower(n.Value.str()) + " " + lower(n.Description.str())
			if !strings.Contains(all, wantText) {
				continue
			}
			if name == wantText {
				score = 0
			} else if score > 3 {
				score = 3
			}
			// text sits in a StaticText child: the thing to act on is its interactive ancestor
			if plainRoles[r] {
				for p, hops := byID[n.ParentID], 0; p != nil && hops < 6; p, hops = byID[p.ParentID], hops+1 {
					if p.interactive() {
						target = p
						break
					}
				}
			}
		}
		if target.BackendDOMNodeID == 0 || seen[target.BackendDOMNodeID] {
			continue
		}
		seen[target.BackendDOMNodeID] = true
		if !target.interactive() && wantRole == "" {
			score += 2 // when no role was asked for, things you can act on come first
		}
		hits = append(hits, hit{target, score, i})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score < hits[j].score })
	var els []Element
	for _, h := range hits {
		if len(els) >= o.Limit {
			break
		}
		els = append(els, h.n.element())
	}
	b.tagUnnamedLocked(els)
	return els, nil
}

func (b *Browser) findCSSLocked(o FindOptions) ([]Element, error) {
	var doc struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := b.pageCall("DOM.getDocument", map[string]any{"depth": 0}, &doc); err != nil {
		return nil, err
	}
	var q struct {
		NodeIDs []int `json:"nodeIds"`
	}
	if err := b.pageCall("DOM.querySelectorAll", map[string]any{"nodeId": doc.Root.NodeID, "selector": o.Selector}, &q); err != nil {
		return nil, fmt.Errorf("selector %q: %w", o.Selector, err)
	}
	var els []Element
	for _, id := range q.NodeIDs {
		if len(els) >= o.Limit {
			break
		}
		var d struct {
			Node struct {
				BackendNodeID int `json:"backendNodeId"`
			} `json:"node"`
		}
		if b.pageCall("DOM.describeNode", map[string]any{"nodeId": id}, &d) != nil {
			continue
		}
		e, err := b.elementOfLocked(d.Node.BackendNodeID)
		if err != nil {
			continue
		}
		els = append(els, e)
	}
	return els, nil
}

// elementOfLocked describes one element by its backend id.
func (b *Browser) elementOfLocked(backend int) (Element, error) {
	var out struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := b.pageCall("Accessibility.getPartialAXTree", map[string]any{"backendNodeId": backend, "fetchRelatives": false}, &out); err != nil {
		return Element{}, staleRef(err)
	}
	for i := range out.Nodes {
		if out.Nodes[i].BackendDOMNodeID == backend {
			e := out.Nodes[i].element()
			if e.Name == "" {
				e.Tag = b.tagOfLocked(backend)
			}
			return e, nil
		}
	}
	return Element{Ref: refOf(backend), Role: "generic", Tag: b.tagOfLocked(backend)}, nil
}

func staleRef(err error) error {
	if err != nil && (strings.Contains(err.Error(), "No node") || strings.Contains(err.Error(), "Could not find node") || strings.Contains(err.Error(), "does not belong")) {
		return errors.New("that ref no longer exists (the page changed): list the elements again")
	}
	return err
}

// FormatElements renders elements one per line for a model: [ref] role "name" states.
func FormatElements(els []Element, indent bool) string {
	var sb strings.Builder
	for _, e := range els {
		if indent {
			sb.WriteString(strings.Repeat("  ", min(e.Depth, 8)))
		}
		fmt.Fprintf(&sb, "[%s] %s", e.Ref, e.Role)
		if e.Role == "heading" && e.Level > 0 {
			fmt.Fprintf(&sb, "%d", e.Level)
		}
		if e.Name != "" {
			fmt.Fprintf(&sb, " %q", clip(e.Name, 120))
		} else {
			sb.WriteString(" (no accessible name)")
		}
		if e.Value != "" && e.Value != e.Name {
			fmt.Fprintf(&sb, " value=%q", clip(e.Value, 80))
		}
		if len(e.States) > 0 {
			sb.WriteString(" {" + strings.Join(e.States, ",") + "}")
		}
		if e.URL != "" {
			fmt.Fprintf(&sb, " -> %s", clip(e.URL, 100))
		}
		if e.Description != "" {
			fmt.Fprintf(&sb, " (%s)", clip(e.Description, 80))
		}
		if e.Tag != "" {
			sb.WriteString(" " + e.Tag)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
