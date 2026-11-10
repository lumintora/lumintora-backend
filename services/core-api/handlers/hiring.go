package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"lumintora/pkg/httputil"
)

// HiringHandler powers the Big-tech Hiring Loop: it serves the loop
// definition, scores a typed answer for a stage against a rubric, and returns
// a hiring-committee verdict. Deterministic + offline (no LLM required).
type HiringHandler struct{ db *sql.DB }

func NewHiringHandler(db *sql.DB) *HiringHandler { return &HiringHandler{db: db} }

type hiringStage struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Time    string   `json:"time"`
	Focus   string   `json:"focus"`
	Prompt  string   `json:"prompt"`
	Signals []string `json:"signals"`
}

var hiringStages = []hiringStage{
	{
		ID: "phone", Label: "Phone screen", Time: "45 min",
		Focus:   "A warm-up problem and a read on how you communicate.",
		Prompt:  "Given a stream of numbers, return the running median at any point. Walk me through your approach before you write any code.",
		Signals: []string{"Problem solving", "Communication", "Coding"},
	},
	{
		ID: "coding", Label: "Coding", Time: "60 min",
		Focus:   "A medium/hard problem. Correctness, edge cases, and how you test.",
		Prompt:  "Serialize and deserialize a binary tree. Describe your encoding, then the time and space complexity, and how you'd test it.",
		Signals: []string{"Problem solving", "Coding", "Testing rigor"},
	},
	{
		ID: "design", Label: "System design", Time: "60 min",
		Focus:   "Design a real system end to end. Trade-offs over trivia.",
		Prompt:  "Design a URL shortener for 50k RPS. Start with the API, then take me through storage, caching, and failure modes.",
		Signals: []string{"Architecture", "Trade-off reasoning", "Scale instincts"},
	},
	{
		ID: "behavioral", Label: "Behavioral", Time: "45 min",
		Focus:   "How you work with others and handle ambiguity.",
		Prompt:  "Tell me about a time you disagreed with a teammate on a technical decision. What did you do, and how did it end?",
		Signals: []string{"Collaboration", "Ownership", "Ambiguity"},
	},
}

// Keywords that signal real depth per stage/signal (lowercase).
var hiringRubric = map[string]map[string][]string{
	"phone": {
		"Problem solving": {"heap", "two heap", "median", "min-heap", "max-heap", "balance", "insert", "sorted"},
		"Communication":   {"first", "then", "clarify", "assume", "example", "edge", "ask", "because"},
		"Coding":          {"function", "add", "return", "o(", "log", "time", "space"},
	},
	"coding": {
		"Problem solving": {"traversal", "preorder", "bfs", "dfs", "recursion", "queue", "null", "marker"},
		"Coding":          {"serialize", "deserialize", "string", "split", "encode", "return", "o("},
		"Testing rigor":   {"test", "edge", "empty", "single", "unbalanced", "roundtrip", "null"},
	},
	"design": {
		"Architecture":        {"api", "load balancer", "cache", "database", "redis", "replica", "cdn", "queue"},
		"Trade-off reasoning": {"trade", "consistency", "availability", "because", "versus", "instead", "cost", "latency"},
		"Scale instincts":     {"shard", "partition", "replica", "throughput", "qps", "rps", "hot", "read heavy", "bottleneck"},
	},
	"behavioral": {
		"Collaboration": {"we", "team", "listen", "agree", "compromise", "pair", "review", "together"},
		"Ownership":     {"i ", "owned", "decided", "drove", "led", "responsib", "result", "shipped"},
		"Ambiguity":     {"unclear", "ambig", "unknown", "explore", "prototype", "data", "assume", "iterate"},
	},
}

// Loop returns the stage definitions (public).
func (h *HiringHandler) Loop(w http.ResponseWriter, r *http.Request) {
	httputil.OK(w, map[string]any{"stages": hiringStages})
}

type signalScore struct {
	Label string `json:"label"`
	Score int    `json:"score"`
}

type hiringScoreReq struct {
	StageID string `json:"stage_id"`
	Answer  string `json:"answer"`
}

type hiringScoreResp struct {
	Signals  []signalScore `json:"signals"`
	Overall  int           `json:"overall"`
	Feedback string        `json:"feedback"`
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Score grades a typed answer for one stage (public).
func (h *HiringHandler) Score(w http.ResponseWriter, r *http.Request) {
	var req hiringScoreReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var stage *hiringStage
	for i := range hiringStages {
		if hiringStages[i].ID == req.StageID {
			stage = &hiringStages[i]
			break
		}
	}
	if stage == nil {
		httputil.Error(w, "unknown stage", http.StatusBadRequest)
		return
	}

	ans := strings.ToLower(req.Answer)
	words := len(strings.Fields(req.Answer))
	rub := hiringRubric[stage.ID]

	signals := make([]signalScore, 0, len(stage.Signals))
	total := 0
	for _, label := range stage.Signals {
		kws := rub[label]
		hits := 0
		for _, kw := range kws {
			if strings.Contains(ans, kw) {
				hits++
			}
		}
		coverage := 0.0
		if len(kws) > 0 {
			coverage = float64(hits) / float64(len(kws))
		}
		score := 42 + int(coverage*48)        // 42..90 from concept coverage
		score += clampInt(words/14, 0, 9)     // up to +9 for a thorough answer
		if words < 8 {                        // too short to show signal
			score = clampInt(score, 0, 38)
		}
		score = clampInt(score, 0, 98)
		signals = append(signals, signalScore{Label: label, Score: score})
		total += score
	}

	overall := 0
	if len(signals) > 0 {
		overall = total / len(signals)
	}

	var feedback string
	switch {
	case words < 8:
		feedback = "Too thin to score well — lay out your approach step by step and name the key trade-offs."
	case overall >= 82:
		feedback = "Strong answer — clear structure and the right core ideas. This is a hire signal."
	case overall >= 68:
		feedback = "Solid, but tighten it: state complexity, edge cases, and the trade-offs explicitly."
	default:
		feedback = "Mixed signal — cover the core concepts for this round and justify your decisions out loud."
	}

	httputil.OK(w, hiringScoreResp{Signals: signals, Overall: overall, Feedback: feedback})
}

type hiringDecideReq struct {
	Scores []int `json:"scores"`
}

type hiringDecideResp struct {
	Decision string `json:"decision"`
	Level    string `json:"level"`
	Overall  int    `json:"overall"`
	Summary  string `json:"summary"`
}

// Decide returns the hiring-committee verdict from the stage scores (public).
func (h *HiringHandler) Decide(w http.ResponseWriter, r *http.Request) {
	var req hiringDecideReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	sum, n := 0, 0
	for _, s := range req.Scores {
		sum += s
		n++
	}
	overall := 0
	if n > 0 {
		overall = sum / n
	}

	var decision, level, summary string
	switch {
	case n == 0:
		decision, level = "Incomplete", "—"
		summary = "No stages were completed — run the loop end to end to get a decision."
	case overall >= 85:
		decision, level = "Strong Hire", "Senior · L5"
		summary = "Consistent top-of-bar signal across the loop. The committee would move to an offer."
	case overall >= 75:
		decision, level = "Hire", "Mid–Senior · L4"
		summary = "Clears the bar with solid signal in every round. Likely an offer after packet review."
	case overall >= 63:
		decision, level = "Leaning Hire", "Mid · L4"
		summary = "On the bar but uneven — the committee would debate this and may ask for a follow-up."
	default:
		decision, level = "No Hire", "Below bar"
		summary = "Signal didn't clear the bar this loop. Strengthen the weak rounds and run it again."
	}

	httputil.OK(w, hiringDecideResp{Decision: decision, Level: level, Overall: overall, Summary: summary})
}
