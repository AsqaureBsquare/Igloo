package api

import (
	"strings"

	"github.com/igloo-dev/igloo/internal/models"
)

// WorkspaceFilter holds parsed search criteria from a ?q= query string.
// Format mirrors Coder: space-separated key:value pairs.
// Bare words (no colon) match workspace name.
//
// Supported keys: owner, template, name, status, org
// Special owner value: "me" → resolved by the caller to the current user ID
type WorkspaceFilter struct {
	Owner    string // username or user ID; "me" = current user
	Template string // template name (partial match)
	Name     string // workspace name (partial match)
	Status   string // running | stopped | creating | deleting | error
	Org      string // org ID or org name (partial match)
}

// ParseWorkspaceFilter parses a Coder-style ?q= search string.
func ParseWorkspaceFilter(q string) WorkspaceFilter {
	var f WorkspaceFilter
	for _, term := range tokenise(q) {
		key, val, ok := strings.Cut(term, ":")
		if !ok {
			// Bare word → name filter
			f.Name = term
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "owner":
			f.Owner = strings.TrimSpace(val)
		case "template":
			f.Template = strings.ToLower(strings.TrimSpace(val))
		case "name":
			f.Name = strings.ToLower(strings.TrimSpace(val))
		case "status":
			f.Status = strings.ToLower(strings.TrimSpace(val))
		case "org", "organization":
			f.Org = strings.ToLower(strings.TrimSpace(val))
		}
	}
	return f
}

// tokenise splits a query string by spaces, respecting double-quoted strings.
func tokenise(q string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	for _, ch := range q {
		switch {
		case ch == '"':
			inQuote = !inQuote
		case ch == ' ' && !inQuote:
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(ch)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// applyWorkspaceFilter filters a workspace list in-memory using a WorkspaceFilter.
// templateNames is a map of template ID → template name for name-based matching.
func applyWorkspaceFilter(ws []*models.Workspace, f WorkspaceFilter, templateNames map[string]string) []*models.Workspace {
	if f.Owner == "" && f.Template == "" && f.Name == "" && f.Status == "" && f.Org == "" {
		return ws
	}
	out := ws[:0:0]
	for _, w := range ws {
		if f.Owner != "" && f.Owner != "me" {
			if !containsCI(w.UserID, f.Owner) {
				continue
			}
		}
		if f.Template != "" {
			tName := strings.ToLower(templateNames[w.TemplateID])
			if !strings.Contains(tName, f.Template) {
				continue
			}
		}
		if f.Name != "" && !containsCI(w.Name, f.Name) {
			continue
		}
		if f.Status != "" && string(w.Status) != f.Status {
			continue
		}
		if f.Org != "" && !containsCI(w.OrgID, f.Org) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// TemplateFilter holds parsed search criteria for templates.
type TemplateFilter struct {
	Name string
}

// ParseTemplateFilter parses a ?q= string for templates.
func ParseTemplateFilter(q string) TemplateFilter {
	var f TemplateFilter
	for _, term := range tokenise(q) {
		key, val, ok := strings.Cut(term, ":")
		if !ok {
			f.Name = term
			continue
		}
		if strings.ToLower(key) == "name" {
			f.Name = strings.TrimSpace(val)
		}
	}
	return f
}

func applyTemplateFilter(ts []*models.Template, f TemplateFilter) []*models.Template {
	if f.Name == "" {
		return ts
	}
	out := ts[:0:0]
	for _, t := range ts {
		if containsCI(t.Name, f.Name) || containsCI(t.Description, f.Name) {
			out = append(out, t)
		}
	}
	return out
}

func containsCI(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
