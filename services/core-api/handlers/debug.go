package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"lumintora/pkg/httputil"
)

// DebugHandler powers the AI Debugging page: it serves a broken-code challenge
// and gives progressive hints. The AI deliberately never writes the fix — it
// only guides. The actual test run happens client-side (JavaScript).
type DebugHandler struct{ db *sql.DB }

func NewDebugHandler(db *sql.DB) *DebugHandler { return &DebugHandler{db: db} }

type debugTest struct {
	Name   string `json:"name"`
	Args   []any  `json:"args"`
	Expect any    `json:"expect"`
}

type debugChallenge struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Language    string      `json:"language"`
	File        string      `json:"file"`
	Description string      `json:"description"`
	BuggyCode   string      `json:"buggy_code"`
	Entry       string      `json:"entry"`
	Tests       []debugTest `json:"tests"`
	MaxHints    int         `json:"max_hints"`
}

// Challenge returns the broken-code challenge (public).
func (h *DebugHandler) Challenge(w http.ResponseWriter, r *http.Request) {
	items := make([]any, 30)
	for i := range items {
		items[i] = i + 1
	}
	page := func(p int) []any {
		out := make([]any, 0, 10)
		for i := p * 10; i < p*10+10; i++ {
			out = append(out, items[i])
		}
		return out
	}

	httputil.OK(w, debugChallenge{
		ID:          "paginate",
		Title:       "Pagination drops an item",
		Language:    "javascript",
		File:        "paginate.js",
		Description: "Every page comes back one item short. Find and fix the bug — I'll help you reason it out, but you write the fix.",
		BuggyCode:   "function paginate(items, page, size) {\n  const start = page * size\n  const end = start + size\n  return items.slice(start, end - 1)\n}",
		Entry:       "paginate",
		Tests: []debugTest{
			{Name: "first page returns 10 items", Args: []any{items, 0, 10}, Expect: page(0)},
			{Name: "second page is full", Args: []any{items, 1, 10}, Expect: page(1)},
			{Name: "last page is full", Args: []any{items, 2, 10}, Expect: page(2)},
		},
		MaxHints: 3,
	})
}

type debugHintReq struct {
	ID       string `json:"id"`
	Level    int    `json:"level"`
	Question string `json:"question"`
}

type debugHintResp struct {
	Hint      string `json:"hint"`
	Level     int    `json:"level"`
	Exhausted bool   `json:"exhausted"`
}

var debugHints = map[string][]string{
	"paginate": {
		"Run the tests first and compare what they expect with what you get — how many items come back per page, versus how many should?",
		"Look only at the return line. Array.prototype.slice(start, end) — is that second argument inclusive or exclusive of end?",
		"slice(start, end) already stops *before* end — it's end-exclusive. So the `- 1` is cutting one item too many. I won't write the corrected line for you, but you now know exactly what to change.",
	},
}

// Hint returns a progressive nudge — never the actual fix (public).
func (h *DebugHandler) Hint(w http.ResponseWriter, r *http.Request) {
	var req debugHintReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	hints := debugHints[req.ID]
	if hints == nil {
		hints = debugHints["paginate"]
	}

	// If the user asks us to just fix / write the code, we decline but still nudge.
	q := strings.ToLower(req.Question)
	refuses := strings.Contains(q, "fix it") || strings.Contains(q, "solution") ||
		strings.Contains(q, "answer") || strings.Contains(q, "write") || strings.Contains(q, "just give")

	lvl := req.Level
	if lvl < 0 {
		lvl = 0
	}

	if lvl >= len(hints) {
		httputil.OK(w, debugHintResp{
			Hint:      "That's as far as I'll nudge — you've got the insight now. Make the edit and run the tests. You've got this.",
			Level:     lvl,
			Exhausted: true,
		})
		return
	}

	hint := hints[lvl]
	if refuses {
		hint = "I can guide you, but I won't write the fix for you — that's the part you learn from. Here's a nudge instead: " + hint
	}
	httputil.OK(w, debugHintResp{Hint: hint, Level: lvl + 1, Exhausted: lvl+1 >= len(hints)})
}
