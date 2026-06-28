package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"lumintora/pkg/httputil"
)

const chatSystemPrompt = `You are Lumi, the helpful assistant for Lumintora — an AI-powered adaptive learning platform.

You ONLY answer questions about Lumintora and topics directly relevant to using it:
- What Lumintora is and how it works
- Features: adaptive learning paths, AI tutor, coding judge, quizzes, XP & streaks, leaderboard
- How to get started, create an account, build a learning path
- Supported topics (DSA, Web Dev, Python, Java, etc.)
- Pricing (free during beta)
- Technical doubts about using the platform
- Contact and support

If the user asks ANYTHING unrelated to Lumintora or learning on this platform, politely say:
"I'm Lumi, Lumintora's assistant. I can only help with questions about Lumintora and learning on our platform. For anything else, feel free to reach us at lumintoraai@gmail.com!"

Keep replies concise (2–4 sentences max). Be friendly and encouraging.`

type ChatHandler struct{ ai *AIHandler }

func NewChatHandler(ai *AIHandler) *ChatHandler { return &ChatHandler{ai: ai} }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (h *ChatHandler) Chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string        `json:"message"`
		History []chatMessage `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		httputil.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	// Build conversation context (last 6 turns max to keep prompt short)
	history := req.History
	if len(history) > 6 {
		history = history[len(history)-6:]
	}

	var contextLines []string
	for _, m := range history {
		role := "User"
		if m.Role == "assistant" {
			role = "Lumi"
		}
		contextLines = append(contextLines, role+": "+m.Content)
	}

	prompt := chatSystemPrompt
	if len(contextLines) > 0 {
		prompt += "\n\nConversation so far:\n" + strings.Join(contextLines, "\n")
	}
	prompt += "\n\nUser: " + req.Message + "\nLumi:"

	reply, err := h.ai.callCFAI("/ai/generate", map[string]string{"prompt": prompt})
	if err != nil {
		httputil.Error(w, "could not get a response right now", http.StatusInternalServerError)
		return
	}

	httputil.OK(w, map[string]string{"reply": strings.TrimSpace(reply)})
}
