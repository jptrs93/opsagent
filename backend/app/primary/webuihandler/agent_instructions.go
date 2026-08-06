package webuihandler

import (
	_ "embed"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"text/template"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

//go:embed agent_instructions.md
var agentInstructionsMarkdown string

var agentInstructionsTemplate = template.Must(template.New("agent-instructions").Parse(agentInstructionsMarkdown))

var AgentInstructionsUserRequiredErr = apigen.NewApiErr("A user_id query parameter is required", "agent_instructions_user_required", http.StatusBadRequest)

type agentInstructionsData struct {
	BaseURL string
	UserID  int32
}

// GetV1AgentSessionsInstructions renders the API instructions an agent needs to
// drive this server. Unauthenticated on purpose: it is the single URL an
// operator hands to an agent, and it grants nothing — the agent still has to
// request a session and wait for that same operator to approve it.
//
// user_id only decides whose approval queue a later request-start lands in. It
// is validated here so a mistyped URL fails immediately rather than at the
// first API call, hours of agent work later.
func (h *Handler) GetV1AgentSessionsInstructions(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	raw := strings.TrimSpace(request.URL.Query().Get("user_id"))
	if raw == "" {
		return AgentInstructionsUserRequiredErr
	}
	userID, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return AgentSessionUserNotFoundErr
	}
	user, err := h.Store.FetchUserMatching(func(u *apigen.InternalUser) bool { return u.ID == int32(userID) })
	if errors.Is(err, sqlite.ErrNotFound) {
		return AgentSessionUserNotFoundErr
	}
	if err != nil {
		return fmt.Errorf("resolving user for agent instructions: %w", err)
	}

	var body strings.Builder
	if err := agentInstructionsTemplate.Execute(&body, agentInstructionsData{
		BaseURL: requestBaseURL(request),
		UserID:  user.ID,
	}); err != nil {
		return fmt.Errorf("rendering agent instructions: %w", err)
	}

	writer.Header().Set("X-Content-Type-Options", "nosniff")
	// Agents fetch this and read it as text; browsers get a wrapper so a click
	// on the link in the UI renders something legible rather than downloading.
	if prefersHTML(request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err = writer.Write([]byte(wrapInstructionsHTML(body.String())))
		return err
	}
	writer.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, err = writer.Write([]byte(body.String()))
	return err
}

// requestBaseURL reconstructs the address the caller reached us on, which is
// the one the agent should keep using. This mirrors how the web UI derives the
// same value from window.location.origin.
func requestBaseURL(request *http.Request) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + request.Host
}

// prefersHTML reports whether the caller is a browser. curl and HTTP libraries
// send either no Accept header or */*, and both should get the markdown.
func prefersHTML(request *http.Request) bool {
	return strings.Contains(request.Header.Get("Accept"), "text/html")
}

func wrapInstructionsHTML(markdown string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<title>OpenDeploy API instructions</title>` +
		`<style>body{margin:0;background:#111827;color:#e5e7eb;font:14px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace}` +
		`pre{margin:0 auto;max-width:80ch;padding:2rem 1rem;white-space:pre-wrap;word-wrap:break-word}</style>` +
		`</head><body><pre>` + html.EscapeString(markdown) + `</pre></body></html>`
}
