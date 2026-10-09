package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Actions are never executed here. go-svc validates and audits the intent,
// then hands it to agent-svc, which runs the Claude CLI. This service's
// ServiceAccount has no write verbs at all, so a bug in this path cannot
// change cluster state.

// hardDeniedNamespaces are refused regardless of configuration.
var hardDeniedNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

var actionLabels = map[string]string{
	"delete-pod": "Delete pod",
	"ask":        "Ask about a resource",
}

// actionsNeedingPrompt carry free-form user text. Their safety comes from the
// agent's per-invocation tool allowlist and from RBAC, not from this gate.
var actionsNeedingPrompt = map[string]bool{"ask": true}

// objectNamePattern is a DNS-1123 subdomain, which covers pod names.
var objectNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// kindPattern matches the kubectl resource names used as the UI's kind keys.
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

const maxPromptChars = 2000

type agent struct {
	baseURL string
	client  *http.Client
	allowed map[string]bool
	denied  map[string]bool
}

func envSet(name string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Split(os.Getenv(name), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out[v] = true
		}
	}
	return out
}

func newAgent() *agent {
	denied := envSet("DENIED_NAMESPACES")
	for ns := range hardDeniedNamespaces {
		denied[ns] = true
	}
	return &agent{
		baseURL: strings.TrimSuffix(os.Getenv("AGENT_URL"), "/"),
		client:  &http.Client{Timeout: 15 * time.Second},
		allowed: envSet("ALLOWED_NAMESPACES"),
		denied:  denied,
	}
}

func (a *agent) configured() bool { return a.baseURL != "" }

// policyCheck is the gate: an action must name a valid object in an allowed,
// non-denied namespace.
func (a *agent) policyCheck(action, namespace, name string) (int, error) {
	if _, ok := actionLabels[action]; !ok {
		return http.StatusBadRequest, fmt.Errorf("unknown action %q", action)
	}
	if !validNamespace(namespace) {
		return http.StatusBadRequest, fmt.Errorf("invalid namespace name")
	}
	if !objectNamePattern.MatchString(name) {
		return http.StatusBadRequest, fmt.Errorf("invalid object name")
	}
	if a.denied[namespace] {
		return http.StatusForbidden, fmt.Errorf("namespace %q is denied", namespace)
	}
	if !a.allowed[namespace] {
		return http.StatusForbidden, fmt.Errorf("namespace %q is not in the allowed list", namespace)
	}
	return http.StatusOK, nil
}

// forward proxies to agent-svc, passing its status code and body through.
func (a *agent) forward(w http.ResponseWriter, method, path string, body any) {
	if !a.configured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "agent is not configured (AGENT_URL unset)",
		})
		return
	}

	var payload io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		payload = bytes.NewReader(buf)
	}

	req, err := http.NewRequest(method, a.baseURL+path, payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := a.client.Do(req)
	if err != nil {
		log.Printf("agent request failed: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "agent unreachable: " + err.Error(),
		})
		return
	}
	defer res.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	w.Write(raw)
}

type actionRequest struct {
	Action    string `json:"action"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Prompt    string `json:"prompt"`
	SessionID string `json:"sessionId"`
}

func (s *server) handleAction(w http.ResponseWriter, r *http.Request) {
	var req actionRequest
	// Generous enough for a 2000-char prompt plus identifiers.
	if err := json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request body"})
		return
	}

	deny := func(status int, reason string) {
		// Audit denials as loudly as approvals.
		log.Printf("AUDIT action=%s namespace=%s name=%s decision=denied reason=%q remote=%s",
			req.Action, req.Namespace, req.Name, reason, r.RemoteAddr)
		writeJSON(w, status, map[string]string{"error": reason})
	}

	if status, err := s.agent.policyCheck(req.Action, req.Namespace, req.Name); err != nil {
		deny(status, err.Error())
		return
	}

	req.Prompt = strings.TrimSpace(req.Prompt)
	if actionsNeedingPrompt[req.Action] {
		if req.Prompt == "" {
			deny(http.StatusBadRequest, "prompt is required")
			return
		}
		if len(req.Prompt) > maxPromptChars {
			deny(http.StatusBadRequest, fmt.Sprintf("prompt exceeds %d characters", maxPromptChars))
			return
		}
	}
	if req.Kind != "" && !kindPattern.MatchString(req.Kind) {
		deny(http.StatusBadRequest, "invalid kind")
		return
	}
	if req.SessionID != "" && !sessionPattern.MatchString(req.SessionID) {
		deny(http.StatusBadRequest, "invalid session id")
		return
	}

	requestID := fmt.Sprintf("%d", time.Now().UnixNano())
	// The prompt is user text: record its length, not its content.
	log.Printf("AUDIT action=%s kind=%s namespace=%s name=%s promptChars=%d decision=allowed requestId=%s remote=%s",
		req.Action, req.Kind, req.Namespace, req.Name, len(req.Prompt), requestID, r.RemoteAddr)

	s.agent.forward(w, http.MethodPost, "/agent/tasks", map[string]string{
		"action":    req.Action,
		"namespace": req.Namespace,
		"name":      req.Name,
		"kind":      req.Kind,
		"prompt":    req.Prompt,
		"sessionId": req.SessionID,
		"requestId": requestID,
	})
}

func (s *server) handleTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || len(id) > 64 || strings.ContainsAny(id, "/?#") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid task id"})
		return
	}
	s.agent.forward(w, http.MethodGet, "/agent/tasks/"+url.PathEscape(id), nil)
}

func (s *server) handleAgentStatus(w http.ResponseWriter, _ *http.Request) {
	s.agent.forward(w, http.MethodGet, "/agent/status", nil)
}
