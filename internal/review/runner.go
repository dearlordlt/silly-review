package review

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Options configures a single claude review invocation.
type Options struct {
	Model           string
	System          string // --append-system-prompt
	Prompt          string // -p
	PrimaryWorktree string // cwd for the subprocess
	OtherWorktrees  []string
	BinPath         string // claude binary; defaults to "claude"
}

// allowedTools / disallowedTools enforce the read-only sandbox. Kept as package
// vars so the golden test can assert them.
var allowedTools = []string{
	"Read", "Grep", "Glob",
	"Bash(git log:*)", "Bash(git show:*)", "Bash(git diff:*)", "Bash(git blame:*)",
	"Bash(git status:*)", "Bash(git rev-parse:*)", "Bash(rg:*)", "Bash(cat:*)", "Bash(ls:*)",
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
		"--output-format", "stream-json", "--verbose",
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
	return args
}

// EventKind tags a progress event.
type EventKind int

const (
	EvtText EventKind = iota
	EvtTool
	EvtRetry
	EvtResult
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
	CostUSD           float64
	PermissionDenials []string
}

// streamLine is the union of stream-json event shapes we care about.
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
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
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var sl streamLine
		if json.Unmarshal(sc.Bytes(), &sl) != nil {
			continue
		}
		switch sl.Type {
		case "system":
			if sl.Subtype == "api_retry" {
				reason := sl.Error
				if reason == "" {
					reason = "transient error"
				}
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

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	res.Stderr = strings.TrimSpace(stderr.String())

	// claude never emitted a result event (crashed, killed, or died before the
	// API call). Surface stderr/exit so the user sees the real reason instead of
	// a silent "no findings".
	if !sawResult {
		msg := firstNonEmpty(res.Stderr, errString(waitErr), "claude exited without producing a review")
		return res, fmt.Errorf("claude failed: %s", msg)
	}
	// A result arrived but flagged an error (e.g. auth/API error). Guarantee a
	// non-empty message — the text can live in result, or only on stderr.
	if res.IsError {
		res.ErrMsg = firstNonEmpty(res.RawText, res.Stderr, "claude reported an error (run with --debug for detail)")
	}
	return res, nil
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
