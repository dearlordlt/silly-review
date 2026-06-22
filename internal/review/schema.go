package review

// Finding is one review comment. Field tags mirror SchemaJSON so the validated
// structured_output unmarshals directly.
type Finding struct {
	Repo        string `json:"repo,omitempty"`
	File        string `json:"file"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line,omitempty"`
	Side        string `json:"side,omitempty"`
	Severity    string `json:"severity"`
	Category    string `json:"category,omitempty"`
	Title       string `json:"title"`
	Comment     string `json:"comment"`
	CodeSnippet string `json:"code_snippet"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// Review is the full structured result for one claude invocation.
type Review struct {
	Summary  string    `json:"summary"`
	Verdict  string    `json:"verdict"`
	Findings []Finding `json:"findings"`
}

// SeverityRank orders severities most-important first for sorting/filtering.
func SeverityRank(sev string) int {
	switch sev {
	case "blocker":
		return 0
	case "major":
		return 1
	case "minor":
		return 2
	case "nit":
		return 3
	case "question":
		return 4
	case "praise":
		return 5
	default:
		return 6
	}
}

// SchemaJSON is passed to `claude --json-schema`. The model fills structured_output
// to match it.
const SchemaJSON = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "verdict", "findings"],
  "properties": {
    "summary": { "type": "string", "description": "A substantive overall assessment in a senior engineer's voice (roughly 4-8 sentences): what you examined, what is solid, and any concerns. Write this even when there are no blocking findings, so the review visibly did real work." },
    "verdict": { "type": "string", "enum": ["approve", "approve_with_nits", "request_changes", "block"] },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["repo", "file", "start_line", "severity", "title", "comment", "code_snippet"],
        "properties": {
          "repo": { "type": "string", "description": "name of the repo this finding belongs to" },
          "file": { "type": "string", "description": "path relative to that repo's root, new side of the diff" },
          "start_line": { "type": "integer", "description": "1-based line number on the NEW side" },
          "end_line": { "type": "integer" },
          "side": { "type": "string", "enum": ["new", "old"] },
          "severity": { "type": "string", "enum": ["blocker", "major", "minor", "nit", "question", "praise"] },
          "category": { "type": "string", "enum": ["correctness", "security", "performance", "design", "style", "test", "docs", "other"] },
          "title": { "type": "string", "description": "one-line headline" },
          "comment": { "type": "string", "description": "the review comment, copy-paste ready markdown, no filler" },
          "code_snippet": { "type": "string", "description": "verbatim source line(s) at file:start_line so the reviewer can Ctrl-F to it in the PR" },
          "suggestion": { "type": "string", "description": "optional concrete replacement code" }
        }
      }
    }
  }
}`
