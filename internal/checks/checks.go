// Package checks implements codebase health checks: whole-repo audits through
// a chosen lens (security, tech debt, performance, …) rather than a branch
// diff. Each finding carries a concrete solution and a self-contained fix
// prompt the user can paste into an AI coding agent (Claude Code, Cursor, …).
package checks

import (
	"fmt"
	"strings"
)

// Scope narrows a category to a sub-area (or covers all of it: the first scope
// of every category is the broad "general" pass).
type Scope struct {
	Key      string
	Name     string
	Desc     string
	Addendum string // appended to the category focus in the system prompt
}

// Category is one selectable audit lens.
type Category struct {
	Key    string
	Name   string
	Desc   string
	Focus  string // the lens-specific system-prompt body
	Scopes []Scope
}

// Categories are the presets shown in the picker. Order matters (first = default).
var Categories = []Category{
	{
		Key:  "security",
		Name: "Security",
		Desc: "Auth, injection, secrets, unsafe input, data exposure.",
		Focus: "Audit through a security lens. Hunt for: broken or missing authentication/authorization checks (unprotected endpoints, IDOR, privilege escalation, tenant leakage), injection of any kind (SQL, command, path traversal, template, header), unsafe deserialization or parsing of untrusted input, SSRF, secrets committed to the repo or leaked into logs/errors/URLs, weak or homegrown crypto, insecure defaults (debug endpoints, permissive CORS, cookies without flags, missing TLS verification), and sensitive data exposure in logs, errors, or API responses. " +
			"Rate severity by real-world exploitability and blast radius, not theory.",
		Scopes: []Scope{
			{Key: "general", Name: "General", Desc: "The whole attack surface.",
				Addendum: "Cover the whole attack surface, prioritizing entry points: routes/handlers, auth middleware, input parsing, subprocess/external calls, and secrets handling."},
			{Key: "auth", Name: "Auth & access control", Desc: "Login, sessions, permissions, IDOR, tenant isolation.",
				Addendum: "Narrow to authentication and authorization only: login/session/token handling, whether every endpoint and query actually enforces permissions (IDOR), role and tenant isolation, privilege-escalation paths, and how passwords/tokens are stored and compared."},
			{Key: "input", Name: "Injection & input handling", Desc: "Untrusted data flowing into queries, shells, paths, templates.",
				Addendum: "Narrow to untrusted input: find every place external data enters (HTTP params, headers, cookies, files, webhooks, queue messages, env) and trace how it flows into queries, shell commands, file paths, templates, redirects, and deserializers."},
			{Key: "secrets", Name: "Secrets & configuration", Desc: "Committed credentials, secrets in logs, insecure defaults.",
				Addendum: "Narrow to secrets and configuration: hardcoded or committed credentials (also check example/config files and tests), secrets leaked into logs/errors/URLs, .env handling, and insecure config defaults — permissive CORS, cookie flags, TLS settings, debug modes left reachable."},
		},
	},
	{
		Key:  "debt",
		Name: "Tech debt",
		Desc: "Dead code, duplication, complexity, outdated patterns, stale TODOs.",
		Focus: "Audit for technical debt — what makes this codebase slower and riskier to change. Hunt for: dead code and unused exports/flags/config, copy-paste duplication that has drifted (or will), functions/files grown past comprehension, tangled module boundaries and circular dependencies, outdated patterns the codebase has otherwise moved away from, deprecated API usage, stale TODO/FIXME/HACK markers hiding real bugs, and names/comments/docs that lie about current behavior. " +
			"Weigh each by how much it taxes future changes — flag what a team should actually schedule, not cosmetic churn.",
		Scopes: []Scope{
			{Key: "general", Name: "General", Desc: "The full debt picture, prioritized.",
				Addendum: "Cover the full debt picture and prioritize: lead with the debt that most slows down or endangers everyday changes."},
			{Key: "dead", Name: "Dead code & duplication", Desc: "Unused code, copy-paste drift.",
				Addendum: "Narrow to dead code and duplication: unused functions/exports/files/feature flags/config keys, commented-out blocks kept 'just in case', and duplicated logic — especially copies that have already drifted apart in behavior."},
			{Key: "complexity", Name: "Complexity hotspots", Desc: "Oversized units, deep nesting, god objects.",
				Addendum: "Narrow to the hardest-to-change code: oversized functions/files, deep nesting, tangled control flow, god objects, circular dependencies. For each hotspot say why it's risky (blast radius, likelihood of change) and give a safe decomposition path."},
			{Key: "outdated", Name: "Outdated patterns", Desc: "Deprecated APIs, half-finished migrations.",
				Addendum: "Narrow to outdated patterns: deprecated stdlib/framework/library usage, half-finished migrations (flag the stragglers still on the old pattern), and workarounds for problems that no longer exist."},
			{Key: "todos", Name: "TODO/FIXME audit", Desc: "Read every marker; classify and act.",
				Addendum: "Read every TODO, FIXME, HACK, and XXX marker in the codebase. Classify each: still relevant (report it with a real fix), already done (report the stale marker for removal), or obsolete. Prioritize markers that hide actual bugs the code currently depends on."},
		},
	},
	{
		Key:  "performance",
		Name: "Performance",
		Desc: "N+1s, hot-loop waste, missing caching, memory growth.",
		Focus: "Audit for performance problems that matter in production. Hunt for: N+1 queries and query patterns that imply missing indexes, unbounded queries and result sets, work inside hot loops that belongs outside (allocation, I/O, regex compilation), missing or wrong caching, sequential calls that should be concurrent, synchronous blocking I/O on hot paths, oversized payloads, and unbounded memory growth (caches without eviction, listeners never removed). " +
			"Estimate impact honestly — flag what would actually show up in a profile, not micro-optimizations.",
		Scopes: []Scope{
			{Key: "general", Name: "General", Desc: "Whole-codebase pass, hottest paths first.",
				Addendum: "Cover the whole codebase, hottest paths first: request handlers, data access, loops over collections that grow with usage."},
			{Key: "db", Name: "Database & queries", Desc: "N+1s, missing indexes, unbounded scans, chatty ORM use.",
				Addendum: "Narrow to data access: N+1 patterns, filters/sorts/joins that imply missing indexes, SELECT * and over-fetching, unbounded scans, missing pagination, transactions held across slow work, and chatty ORM usage."},
			{Key: "memory", Name: "Memory & allocations", Desc: "Leaks, unbounded caches, hot-path allocation.",
				Addendum: "Narrow to memory: leaks (listeners, goroutines/timers never stopped), caches and maps without eviction, large objects retained longer than needed, and avoidable per-request allocations on hot paths."},
			{Key: "io", Name: "Network & I/O", Desc: "Sequential calls, missing pooling/timeouts, chatty APIs.",
				Addendum: "Narrow to network and I/O: sequential calls that could run concurrently, missing connection pooling or keep-alive, chatty request patterns, repeated fetches that beg for caching, oversized/uncompressed payloads, and synchronous file I/O on hot paths."},
			{Key: "frontend", Name: "Frontend runtime", Desc: "Bundle weight, re-renders, main-thread blocking.",
				Addendum: "Narrow to frontend runtime cost: bundle weight (heavy dependencies, missing code-splitting), unnecessary re-renders, main-thread-blocking work, unoptimized assets, layout thrash, and memoization only where it demonstrably matters."},
		},
	},
	{
		Key:  "tests",
		Name: "Tests & coverage",
		Desc: "Untested critical paths, assertion-free tests, flaky patterns.",
		Focus: "Audit the test suite against the code it is supposed to protect. Hunt for: critical paths with no tests at all (money, auth, data mutation, concurrency), tests that assert nothing real (no-op assertions, snapshot-everything, mocks testing mocks), missing edge- and error-path coverage where the code visibly branches, flaky patterns (sleeps, time/order dependence, shared state), and tests so coupled to implementation details they break on every refactor. " +
			"Prioritize by risk: one untested payment path outranks a hundred untested getters.",
		Scopes: []Scope{
			{Key: "general", Name: "General", Desc: "Coverage gaps and test quality together.",
				Addendum: "Cover both gaps and quality: map the riskiest flows, check each for meaningful coverage, and judge whether the existing tests would actually catch a regression."},
			{Key: "coverage", Name: "Critical-path gaps", Desc: "The riskiest flows with no meaningful tests.",
				Addendum: "Narrow to coverage gaps: identify the highest-stakes flows in this codebase and report each one that lacks a test that would fail if the behavior broke."},
			{Key: "quality", Name: "Test quality", Desc: "Do existing tests prove anything? Flakiness, coupling.",
				Addendum: "Narrow to the existing tests: assertions that don't prove behavior, flaky patterns (sleeps, ordering, shared state, real network/clock), over-mocking, brittle coupling to implementation details, and suites too slow to run habitually."},
		},
	},
	{
		Key:  "resilience",
		Name: "Error handling & resilience",
		Desc: "Swallowed errors, missing timeouts, leaks, partial failures.",
		Focus: "Audit how this code behaves when things go wrong. Hunt for: swallowed or ignored errors (empty catch blocks, discarded error returns, logged-and-forgotten where the caller needed to know), missing timeouts on network/database calls, retries without backoff or idempotency, resources leaked on error paths (connections, files, locks, goroutines), partial-failure states that corrupt data (multi-step writes without transaction or rollback), crashes on malformed input, and missing graceful shutdown. " +
			"For each finding, say what concretely goes wrong when it fires in production.",
		Scopes: []Scope{
			{Key: "general", Name: "General", Desc: "All failure behavior.",
				Addendum: "Cover all failure behavior, prioritizing the paths where a swallowed failure corrupts data or silently loses work."},
			{Key: "errors", Name: "Swallowed errors", Desc: "Ignored/discarded errors the caller needed.",
				Addendum: "Narrow to error propagation: every ignored or swallowed error, catch blocks that hide failures, and places where an error is logged but the operation continues as if it succeeded."},
			{Key: "timeouts", Name: "Timeouts, retries & backpressure", Desc: "Unbounded waits, retry storms.",
				Addendum: "Narrow to time and load: network/database calls without timeouts, retries without backoff/jitter/idempotency (retry storms, duplicate side effects), unbounded queues, and missing backpressure."},
			{Key: "cleanup", Name: "Resource cleanup & shutdown", Desc: "Leaks on error paths, no graceful shutdown.",
				Addendum: "Narrow to resource lifecycle: connections/files/locks not released on error paths, goroutines or background workers that never stop, and shutdown paths that drop in-flight work."},
		},
	},
	{
		Key:  "deps",
		Name: "Dependency health",
		Desc: "Risky, outdated, duplicated, or oversized dependencies.",
		Focus: "Audit the dependency tree. Read the manifests and lockfiles (go.mod, package.json, requirements.txt, Cargo.toml, …) and check what the code actually uses each dependency for. Hunt for: dependencies with a known vulnerability history or that look abandoned, badly outdated majors that block security updates, multiple libraries doing the same job, heavyweight dependencies pulled in for one trivial function, unpinned or loosely pinned versions where reproducibility matters, and licenses incompatible with this project's. " +
			"You cannot query package registries from here — reason from the manifests, lockfiles, and your own knowledge, and say explicitly when a claim needs online verification.",
		Scopes: []Scope{
			{Key: "general", Name: "General", Desc: "The whole dependency tree.",
				Addendum: "Cover the whole dependency tree: risk, freshness, duplication, and weight."},
			{Key: "risk", Name: "Vulnerable & abandoned", Desc: "Known-risky or unmaintained dependencies.",
				Addendum: "Narrow to risk: dependencies you know to have had serious vulnerabilities, ones that appear unmaintained, and pinned versions old enough to predate known fixes. Mark each claim that needs verification against an advisory database."},
			{Key: "bloat", Name: "Bloat & duplication", Desc: "Overlapping or oversized dependencies.",
				Addendum: "Narrow to bloat: multiple dependencies doing the same job, heavyweight packages used for one small function (suggest the stdlib or a leaner path), and dependencies nothing imports anymore."},
		},
	},
	{
		Key:  "observability",
		Name: "Observability",
		Desc: "Could you debug this in production at 3am?",
		Focus: "Audit whether this system can be debugged in production. Hunt for: failure paths that log nothing — or log without context (no ids, no cause, no parameters), noisy logging that drowns signal (per-request debug spam) or leaks sensitive data, missing metrics on the operations that matter (latency/error counts on external calls, queue depths, job outcomes), no correlation/request ids across boundaries, and health/readiness checks that don't reflect real dependencies. " +
			"Judge everything by one question: when this breaks at 3am, does the on-call engineer have what they need?",
		Scopes: []Scope{
			{Key: "general", Name: "General", Desc: "Logs, metrics, and tracing together.",
				Addendum: "Cover logging, metrics, and tracing together, prioritizing the failure paths an on-call engineer would hit first."},
			{Key: "logging", Name: "Logging", Desc: "Silent failures, context-free or leaky logs.",
				Addendum: "Narrow to logging: silent failure paths, logs without actionable context, noisy or duplicate logs, wrong levels, and sensitive data (tokens, passwords, PII) written to logs."},
			{Key: "metrics", Name: "Metrics & tracing", Desc: "Unmeasured operations, no correlation ids.",
				Addendum: "Narrow to metrics and tracing: external calls and jobs with no latency/error measurement, missing correlation or request ids across boundaries, and health checks that don't test real dependencies."},
		},
	},
}

// CategoryByKey returns the category with the given key and whether it exists.
func CategoryByKey(key string) (Category, bool) {
	for _, c := range Categories {
		if c.Key == key {
			return c, true
		}
	}
	return Category{}, false
}

// ScopeByKey returns the scope with the given key within a category, falling
// back to the first (general) scope.
func ScopeByKey(c Category, key string) Scope {
	for _, s := range c.Scopes {
		if s.Key == key {
			return s
		}
	}
	return c.Scopes[0]
}

const basePersona = `You are a staff-level software engineer auditing a codebase for its owners. This is not a PR review — you are examining the code as it stands and producing a prioritized, actionable problem list.

Work like a real auditor:
- Explore before judging: entry points, module layout, manifests (go.mod/package.json/…), config — then the code, along the audit lens. Read AGENTS.md, CLAUDE.md, and .claude/ if present and respect the project's own conventions; do not report deliberate, documented choices as problems.
- Report EVERY real issue you find through this lens — there is no target count. Don't pad with theoretical nitpicks: every finding must name a concrete consequence someone would care about.
- Severity is real-world impact: critical = exploitable / data loss / outage-grade; high = will bite soon or costs real money or time; medium = should be scheduled; low = fix opportunistically; info = worth knowing.
- For each finding give: the exact file and 1-based line, a verbatim code_snippet of that line (so it can be found later), the problem, its concrete impact, a specific solution, and a fix_prompt.
- fix_prompt is the most important field: a complete, self-contained prompt for an AI coding agent (Claude Code, Cursor). The agent will have the repo open but ZERO other context — no access to this audit. Include: the file path(s) and function/line references, what is wrong and why it matters, the exact change to make, constraints (what must not break, project conventions to follow), and how to verify the fix (specific tests to run or add). Imperative voice. Never reference "the audit", "the finding above", or anything outside the prompt itself.
- Always write a substantive summary: what parts of the codebase you examined (and what you skipped), the overall health through this lens, and the headline risks. Set the health field honestly.
- On a large codebase, prioritize by risk: cover the highest-stakes areas deeply and say in the summary what you didn't reach.

You have read-only tools: Read/Grep/Glob plus read-only git. You cannot and must not edit anything.`

// SystemPrompt builds the --append-system-prompt text for a category + scope.
func SystemPrompt(c Category, s Scope) string {
	return basePersona + "\n\nAudit lens for this pass:\n" + c.Focus + "\n\nScope for this pass:\n" + s.Addendum
}

// Context describes the repo being checked, for prompt construction.
type Context struct {
	RepoName     string
	WorktreePath string
	Ref          string // the ref being audited, e.g. "feat/x" or "origin/main"
}

// BuildPrompt produces the -p prompt for a health check. When prior != nil, the
// check continues from that earlier report instead of starting over.
func BuildPrompt(cx Context, c Category, s Scope, prior *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Perform a %s health check (scope: %s) of the following codebase. Produce findings that conform to the provided JSON schema.\n\n", c.Name, s.Name)
	fmt.Fprintf(&b, "## Codebase: %s\n", cx.RepoName)
	fmt.Fprintf(&b, "- worktree (checked out at %s, read-only): %s\n", cx.Ref, cx.WorktreePath)
	b.WriteString("- this is a whole-codebase audit at that ref — there is no diff scope; judge the code as it stands\n")
	fmt.Fprintf(&b, "- explore with Read/Grep/Glob; `cd %s && git log --oneline -n 20` shows recent history\n\n", cx.WorktreePath)

	if prior != nil {
		b.WriteString("## PREVIOUS CHECK of this codebase — continue from it, don't start over\n")
		b.WriteString("You ran this same check before; the code has likely changed since. Re-audit the CURRENT code:\n")
		b.WriteString("- For each prior finding, check the current code: only re-report it if it still stands (keep the same severity/title so it's recognizable). Drop the ones that are now fixed.\n")
		b.WriteString("- Add any NEW issues introduced since.\n")
		b.WriteString("- In the summary, briefly say what was resolved vs. still open since the last check.\n")
		if prior.Health != "" {
			fmt.Fprintf(&b, "Prior health: %s\n", prior.Health)
		}
		if sum := strings.TrimSpace(prior.Summary); sum != "" {
			fmt.Fprintf(&b, "Prior summary: %s\n", sum)
		}
		if len(prior.Findings) > 0 {
			b.WriteString("Prior findings:\n")
			for _, f := range prior.Findings {
				loc := f.File
				if f.StartLine > 0 {
					loc = fmt.Sprintf("%s:%d", f.File, f.StartLine)
				}
				fmt.Fprintf(&b, "  - [%s] %s — %s\n", f.Severity, loc, f.Title)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("Instructions:\n")
	b.WriteString("- Read the worktree's AGENTS.md / CLAUDE.md / .claude/ first and respect documented decisions.\n")
	fmt.Fprintf(&b, "- Set each finding's `repo` to %q.\n", cx.RepoName)
	b.WriteString("- `start_line` is the 1-based line in the file as it exists at this ref; `code_snippet` is that exact line verbatim.\n")
	b.WriteString("- Every finding needs a self-contained `fix_prompt` written for an agent with zero context — it must work pasted alone into a fresh session.\n")
	fmt.Fprintf(&b, "- Prioritize by real impact through the %s lens; cover the highest-risk areas first.\n", c.Name)
	return b.String()
}

// Finding is one health-check result. Field tags mirror SchemaJSON so the
// validated structured_output unmarshals directly.
type Finding struct {
	Repo        string `json:"repo,omitempty"`
	File        string `json:"file"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line,omitempty"`
	Severity    string `json:"severity"`
	Title       string `json:"title"`
	Problem     string `json:"problem"`
	Impact      string `json:"impact"`
	Solution    string `json:"solution"`
	FixPrompt   string `json:"fix_prompt"`
	CodeSnippet string `json:"code_snippet"`
	Effort      string `json:"effort,omitempty"`
}

// Report is the full structured result of one health check.
type Report struct {
	Summary  string    `json:"summary"`
	Health   string    `json:"health"`
	Findings []Finding `json:"findings"`
}

// SeverityRank orders severities most-important first for sorting/filtering.
func SeverityRank(sev string) int {
	switch sev {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	case "info":
		return 4
	default:
		return 5
	}
}

// HealthLabel renders the health enum for humans ("needs_attention" → "needs attention").
func HealthLabel(h string) string {
	return strings.ReplaceAll(h, "_", " ")
}

// SchemaJSON is passed to `claude --json-schema` for health checks.
const SchemaJSON = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "health", "findings"],
  "properties": {
    "summary": { "type": "string", "description": "A substantive assessment in a senior engineer's voice (roughly 4-10 sentences): what you examined (and skipped), the overall health through this lens, and the headline risks. Write this even with zero findings, so the check visibly did real work." },
    "health": { "type": "string", "enum": ["good", "needs_attention", "at_risk", "critical"] },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["repo", "file", "start_line", "severity", "title", "problem", "impact", "solution", "fix_prompt", "code_snippet"],
        "properties": {
          "repo": { "type": "string", "description": "name of the repo this finding belongs to" },
          "file": { "type": "string", "description": "path relative to the repo root" },
          "start_line": { "type": "integer", "description": "1-based line number in the file at the audited ref" },
          "end_line": { "type": "integer" },
          "severity": { "type": "string", "enum": ["critical", "high", "medium", "low", "info"] },
          "title": { "type": "string", "description": "one-line headline" },
          "problem": { "type": "string", "description": "what is wrong, specifically — plain markdown, no filler" },
          "impact": { "type": "string", "description": "the concrete consequence: what breaks, leaks, slows, or costs, and when" },
          "solution": { "type": "string", "description": "how to fix it, for a human reader — the approach, not necessarily full code" },
          "fix_prompt": { "type": "string", "description": "a complete, self-contained prompt for an AI coding agent (Claude Code, Cursor) that will have the repo open but ZERO other context. Include file paths and line/function references, what is wrong and why, the exact change to make, constraints (what must not break, conventions to follow), and how to verify (tests to run or add). Imperative voice. Never reference this audit or anything outside the prompt." },
          "code_snippet": { "type": "string", "description": "verbatim source line(s) at file:start_line so the reader can find the spot" },
          "effort": { "type": "string", "enum": ["quick", "moderate", "large"], "description": "rough fix size: quick = minutes, moderate = an afternoon, large = needs planning" }
        }
      }
    }
  }
}`
