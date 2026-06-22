package review

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Options configures a single claude review invocation.
type Options struct {
	Model           string
	System          string // --append-system-prompt
	Prompt          string // -p
	PrimaryWorktree string // cwd for the subprocess
	OtherWorktrees  []string
	BinPath         string // claude binary; defaults to "claude"
	ResumeSessionID string // when set, resume this session instead of starting fresh
}

// allowedTools / disallowedTools enforce the read-only sandbox. Kept as package
// vars so the golden test can assert them.
var allowedTools = []string{
	"Read", "Grep", "Glob",
	"Bash(git log:*)", "Bash(git show:*)", "Bash(git diff:*)", "Bash(git blame:*)",
	"Bash(git status:*)", "Bash(git rev-parse:*)", "Bash(rg:*)", "Bash(cat:*)", "Bash(ls:*)",
	// Internal, non-filesystem tools the review needs. StructuredOutput is how
	// --json-schema emits the validated result; under dontAsk it must be allowed
	// or the run dies at the final step. TodoWrite is the model's task tracker
	// (no repo writes). Neither weakens the read-only guarantee.
	"StructuredOutput", "TodoWrite",
}

var disallowedTools = []string{
	"Edit", "Write", "NotebookEdit",
	"Bash(git push:*)", "Bash(git commit:*)", "Bash(git checkout:*)", "Bash(git reset:*)",
	"Bash(git add:*)", "Bash(git stash:*)", "Bash(git worktree:*)", "Bash(git rebase:*)",
}

// strippedEnvVars are the parent Claude Code session's delegated-session /
// auth markers. When silly-review runs inside a Claude Code session (or its
// integrated terminal), inheriting these makes a fresh nested `claude`
// invocation fail auth (401) even when `claude auth status` reports logged-in.
// We drop exactly these so claude initializes against the user's normal
// credentials — while deliberately KEEPING provider-selection vars like
// CLAUDE_CODE_USE_BEDROCK / _USE_VERTEX / _USE_FOUNDRY (and their *_SKIP_*_AUTH
// tuners), which 3rd-party-provider users export in their shell to make claude
// work at all.
var strippedEnvVars = map[string]bool{
	"CLAUDECODE":                            true,
	"CLAUDE_CODE_SESSION_ID":                true,
	"CLAUDE_CODE_CHILD_SESSION":             true,
	"CLAUDE_CODE_ENTRYPOINT":                true,
	"CLAUDE_CODE_EXECPATH":                  true,
	"CLAUDE_CODE_OAUTH_SCOPES":              true,
	"CLAUDE_CODE_SDK_HAS_OAUTH_REFRESH":     true,
	"CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH": true,
}

// reviewEnv builds the environment for the claude subprocess.
func reviewEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src)+1)
	for _, kv := range src {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		// Drop session markers, and any inherited copy of the var we set below
		// (so we don't end up with a duplicate).
		if strippedEnvVars[key] || key == "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD" {
			continue
		}
		out = append(out, kv)
	}
	// We do want sibling repos' CLAUDE.md loaded for cross-repo context.
	out = append(out, "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1")
	return out
}

// BuildArgs assembles the claude argv (without the binary name).
func BuildArgs(opts Options) []string {
	args := []string{
		"-p", opts.Prompt,
		"--model", opts.Model,
		"--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--json-schema", SchemaJSON,
		"--permission-mode", "dontAsk",
		"--append-system-prompt", opts.System,
	}
	args = append(args, "--allowedTools")
	args = append(args, allowedTools...)
	args = append(args, "--disallowedTools")
	args = append(args, disallowedTools...)
	for _, d := range opts.OtherWorktrees {
		args = append(args, "--add-dir", d)
	}
	// Resume reuses the prior session (all file reads already in context); the
	// same cwd is required for claude to find the session on disk.
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	return args
}

// ResumePrompt nudges a resumed session to emit the review from existing
// context rather than starting the read pass over.
const ResumePrompt = "The previous attempt was interrupted by an API error before you produced the review. Using everything you have already read, output the review now per the JSON schema — do not start the analysis over."

// resumeBackoffs is the wait before each resume attempt (var so tests can zero it).
var resumeBackoffs = []time.Duration{6 * time.Second, 18 * time.Second}

// RunWithResume runs a review and, if it fails transiently (overload, 5xx, a
// mid-generation drop) with a captured session id, resumes that session instead
// of redoing the whole read pass. Auth failures are not resumed (resuming won't
// fix them). It never resumes more than len(resumeBackoffs) times.
func RunWithResume(ctx context.Context, opts Options, onEvent func(Event)) (*Result, error) {
	if onEvent == nil {
		onEvent = func(Event) {}
	}
	res, err := Run(ctx, opts, onEvent)
	for attempt := 0; attempt < len(resumeBackoffs); attempt++ {
		if !resumable(res, err) {
			break
		}
		wait := resumeBackoffs[attempt]
		onEvent(Event{Kind: EvtRetry, Text: fmt.Sprintf("API error — resuming where it left off in %s (attempt %d)…", fmtWait(wait), attempt+1)})
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(wait):
		}
		ropts := opts
		ropts.ResumeSessionID = res.SessionID
		ropts.Prompt = ResumePrompt
		res, err = Run(ctx, ropts, onEvent)
	}
	return res, err
}

// resumable reports whether a failed run is worth resuming.
func resumable(res *Result, err error) bool {
	if res == nil || res.SessionID == "" {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false // user cancelled / timed out — don't resume
	}
	if !res.IsError && err == nil {
		return false // succeeded
	}
	blob := strings.ToLower(res.ErrMsg + " " + errString(err))
	if strings.Contains(blob, "auth") || strings.Contains(blob, "401") || strings.Contains(blob, "sign in") {
		return false // resuming won't fix authentication
	}
	return true
}

func fmtWait(d time.Duration) string {
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// EventKind tags a progress event.
type EventKind int

const (
	EvtText EventKind = iota
	EvtTool
	EvtRetry
	EvtResult
	EvtThinking // streamed partial text while Claude is generating (status-line only)
)

// Event is a progress update surfaced to the TUI.
type Event struct {
	Kind    EventKind
	Text    string
	Attempt int
}

// Result is the outcome of a review invocation.
type Result struct {
	Review            *Review
	RawText           string
	IsError           bool
	ErrMsg            string
	Stderr            string
	SessionID         string // captured from the init event; enables resume
	CostUSD           float64
	PermissionDenials []string
}

// streamLine is the union of stream-json event shapes we care about.
type streamLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Message   struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	Event struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Attempt           int             `json:"attempt"`
	Error             string          `json:"error"`
	IsError           bool            `json:"is_error"`
	Result            string          `json:"result"`
	StructuredOutput  json.RawMessage `json:"structured_output"`
	TotalCostUSD      float64         `json:"total_cost_usd"`
	PermissionDenials []struct {
		ToolName string `json:"tool_name"`
	} `json:"permission_denials"`
}

// Run invokes claude, streaming progress via onEvent, and returns the parsed
// result. onEvent may be nil.
func Run(ctx context.Context, opts Options, onEvent func(Event)) (*Result, error) {
	if onEvent == nil {
		onEvent = func(Event) {}
	}
	bin := opts.BinPath
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, BuildArgs(opts)...)
	cmd.Dir = opts.PrimaryWorktree
	cmd.Env = reviewEnv()
	cmd.Stdin = nil

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	res := &Result{}
	sawResult := false
	var lastRetryErr, resultSubtype string
	var thinkBuf strings.Builder
	lastThink := 0
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var sl streamLine
		if json.Unmarshal(sc.Bytes(), &sl) != nil {
			continue
		}
		switch sl.Type {
		case "stream_event":
			// Live token stream while Claude generates — surfaced only as a
			// status-line ticker so the UI shows motion during the long, quiet
			// final phase. Throttled so we don't emit an event per token.
			if sl.Event.Delta.Type == "text_delta" && sl.Event.Delta.Text != "" {
				thinkBuf.WriteString(sl.Event.Delta.Text)
				if thinkBuf.Len()-lastThink >= 48 || strings.Contains(sl.Event.Delta.Text, "\n") {
					onEvent(Event{Kind: EvtThinking, Text: tailLine(thinkBuf.String())})
					lastThink = thinkBuf.Len()
				}
			}
		case "system":
			if sl.Subtype == "init" && sl.SessionID != "" {
				res.SessionID = sl.SessionID
			}
			if sl.Subtype == "api_retry" {
				reason := sl.Error
				if reason == "" {
					reason = "transient error"
				}
				lastRetryErr = sl.Error // remember the reason in case the run ultimately fails
				onEvent(Event{Kind: EvtRetry, Attempt: sl.Attempt, Text: fmt.Sprintf("retrying (%s), attempt %d", reason, sl.Attempt)})
			}
		case "assistant":
			for _, c := range sl.Message.Content {
				switch c.Type {
				case "text":
					if t := firstLine(c.Text); t != "" {
						onEvent(Event{Kind: EvtText, Text: t})
					}
				case "tool_use":
					onEvent(Event{Kind: EvtTool, Text: describeTool(c.Name, c.Input)})
				}
			}
		case "result":
			sawResult = true
			res.IsError = sl.IsError
			res.RawText = sl.Result
			res.CostUSD = sl.TotalCostUSD
			resultSubtype = sl.Subtype
			if res.SessionID == "" && sl.SessionID != "" {
				res.SessionID = sl.SessionID
			}
			for _, d := range sl.PermissionDenials {
				res.PermissionDenials = append(res.PermissionDenials, d.ToolName)
			}
			if len(sl.StructuredOutput) > 0 {
				var rv Review
				if json.Unmarshal(sl.StructuredOutput, &rv) == nil {
					res.Review = &rv
				}
			}
			onEvent(Event{Kind: EvtResult, Text: "review complete"})
		}
	}

	scanErr := sc.Err() // e.g. a >16MB or garbled line — don't let it fail silently
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	res.Stderr = strings.TrimSpace(stderr.String())

	// claude never emitted a result event (crashed, killed, scanner error, or
	// died before the API call). Surface the real reason instead of a silent
	// "no findings".
	if !sawResult {
		msg := firstNonEmpty(res.Stderr, errString(scanErr), errString(waitErr), "claude exited without producing a review")
		return res, fmt.Errorf("claude failed: %s", msg)
	}
	// A result arrived but flagged an error (e.g. auth/API error). Guarantee a
	// non-empty message — the text can live in result, or only on stderr.
	if res.IsError {
		res.ErrMsg = friendlyError(res.RawText, res.Stderr, lastRetryErr, resultSubtype)
		if d := uniqStrings(res.PermissionDenials); len(d) > 0 {
			res.ErrMsg += " [blocked tools: " + strings.Join(d, ", ") + "]"
		}
	}
	return res, nil
}

func uniqStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// friendlyError turns whatever signal we captured into something actionable.
// claude often flags an error with an empty result body, leaving the real
// reason in the api_retry events or the result subtype.
func friendlyError(rawText, stderr, retryReason, subtype string) string {
	if s := strings.TrimSpace(rawText); s != "" {
		return s
	}
	if r := classifyRetry(retryReason); r != "" {
		return r
	}
	if s := strings.TrimSpace(stderr); s != "" {
		return s
	}
	if subtype != "" && subtype != "success" {
		return "claude ended with: " + subtype
	}
	return "claude reported an error with no detail — likely a transient API issue, try again"
}

func classifyRetry(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case r == "":
		return ""
	case strings.Contains(r, "overload"):
		return "Claude's API is overloaded (retries exhausted) — try again in a bit"
	case strings.Contains(r, "rate"):
		return "rate limited (retries exhausted) — try again shortly"
	case strings.Contains(r, "auth"):
		return "authentication failed — run `claude` and sign in, then retry"
	case strings.Contains(r, "529"), strings.Contains(r, "503"), strings.Contains(r, "overcapacity"):
		return "Claude's API is temporarily unavailable — try again in a bit"
	default:
		return "API error after retries: " + reason
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func describeTool(name string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	switch name {
	case "Bash":
		if c, ok := m["command"].(string); ok {
			return "running: " + firstLine(c)
		}
		return "running a command"
	case "Read":
		if p, ok := m["file_path"].(string); ok {
			return "reading " + filepath.Base(p)
		}
		return "reading a file"
	case "Grep":
		if p, ok := m["pattern"].(string); ok {
			return "searching for " + p
		}
		return "searching"
	case "Glob":
		return "listing files"
	case "StructuredOutput":
		return "writing the review…"
	case "TodoWrite":
		return "planning…"
	default:
		return "using " + name
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 80
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

// tailLine returns the most recent line of streamed text, capped, so the status
// line shows what Claude is writing right now.
func tailLine(s string) string {
	s = strings.TrimRight(s, " \t\r")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	const max = 64
	r := []rune(s)
	if len(r) > max {
		return "…" + string(r[len(r)-max:])
	}
	return s
}
