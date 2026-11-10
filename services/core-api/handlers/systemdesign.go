package handlers

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"

	"lumintora/pkg/httputil"
)

// SystemDesignHandler serves the System Design Lab challenge and grades a
// submitted architecture against real system-design principles. Stateless —
// the db is kept for parity with the other handlers / future persistence.
type SystemDesignHandler struct{ db *sql.DB }

func NewSystemDesignHandler(db *sql.DB) *SystemDesignHandler { return &SystemDesignHandler{db: db} }

type sdComponent struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
}

type sdChallenge struct {
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	Target     string        `json:"target"`
	TargetRPS  int           `json:"target_rps"`
	Reqs       []string      `json:"reqs"`
	Components []sdComponent `json:"components"`
}

// Challenge returns the active brief + the component palette (public).
func (h *SystemDesignHandler) Challenge(w http.ResponseWriter, r *http.Request) {
	httputil.OK(w, sdChallenge{
		ID:        "url-shortener",
		Title:     "Design a URL shortener",
		Target:    "50,000 requests / second",
		TargetRPS: 50000,
		Reqs: []string{
			"Redirects in under 50ms p99",
			"Reads vastly outnumber writes",
			"≥ 99.9% availability",
			"Survive a single-node failure",
		},
		Components: []sdComponent{
			{Type: "client", Label: "Client", Hint: "Entry point / browser"},
			{Type: "cdn", Label: "CDN", Hint: "Edge cache for static + redirects"},
			{Type: "lb", Label: "Load Balancer", Hint: "Spreads traffic across API servers"},
			{Type: "api", Label: "API Server", Hint: "Stateless compute tier"},
			{Type: "cache", Label: "Redis Cache", Hint: "Absorbs hot reads"},
			{Type: "db", Label: "Postgres", Hint: "Durable datastore"},
			{Type: "queue", Label: "Queue", Hint: "Async / decoupling"},
			{Type: "store", Label: "Object Store", Hint: "Blobs / large objects"},
		},
	})
}

type sdNode struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type sdEvalReq struct {
	Nodes []sdNode   `json:"nodes"`
	Edges [][]string `json:"edges"`
}

type sdFinding struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

type sdEvalResp struct {
	RPS      int         `json:"rps"`
	P99      int         `json:"p99"`
	Avail    float64     `json:"avail"`
	Pass     bool        `json:"pass"`
	Findings []sdFinding `json:"findings"`
}

// Evaluate grades a submitted node+edge graph (public).
func (h *SystemDesignHandler) Evaluate(w http.ResponseWriter, r *http.Request) {
	var req sdEvalReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	httputil.OK(w, evaluateDesign(req))
}

func evaluateDesign(req sdEvalReq) sdEvalResp {
	byID := map[string]string{}
	for _, n := range req.Nodes {
		byID[n.ID] = n.Type
	}
	count := func(t string) int {
		c := 0
		for _, n := range req.Nodes {
			if n.Type == t {
				c++
			}
		}
		return c
	}
	has := func(t string) bool { return count(t) > 0 }
	linked := func(t1, t2 string) bool {
		for _, e := range req.Edges {
			if len(e) < 2 {
				continue
			}
			ta, tb := byID[e[0]], byID[e[1]]
			if (ta == t1 && tb == t2) || (ta == t2 && tb == t1) {
				return true
			}
		}
		return false
	}
	degree := func(id string) int {
		d := 0
		for _, e := range req.Edges {
			if len(e) >= 2 && (e[0] == id || e[1] == id) {
				d++
			}
		}
		return d
	}

	apiN, dbN := count("api"), count("db")
	hasLB, hasCache, hasQueue := has("lb"), has("cache"), has("queue")
	hasClient, hasStore := has("client"), has("store")
	cacheWired := hasCache && linked("cache", "api")
	dbWired := dbN > 0 && linked("db", "api")
	clientReaches := hasClient && (linked("client", "api") || linked("client", "lb") || linked("client", "cdn"))

	// Throughput — LB lets the API tier scale; cache absorbs reads; DB caps writes.
	rps := 0
	if apiN > 0 {
		per, mult := 12000, 1
		if hasLB {
			per, mult = 20000, apiN
		}
		rps = per * mult
	}
	if cacheWired {
		rps = int(math.Round(float64(rps) * 1.8))
	}
	if dbWired && !cacheWired {
		dbCap := 20000
		if dbN >= 2 {
			dbCap = 60000
		}
		if rps > dbCap {
			rps = dbCap
		}
	}

	// p99 latency
	p99 := 0
	switch {
	case cacheWired:
		p99 = 38
	case dbWired:
		p99 = 120
	case apiN > 0:
		p99 = 90
	}

	// Availability — remove SPOFs with LB + multiple API + DB replica.
	avail := 99.0
	if hasLB && apiN >= 2 {
		avail += 0.8
	}
	if dbN >= 2 {
		avail += 0.15
	}
	if avail > 99.99 {
		avail = 99.99
	}

	findings := []sdFinding{}
	add := func(level, text string) { findings = append(findings, sdFinding{Level: level, Text: text}) }
	if apiN == 0 {
		add("bad", "No compute tier — add an API server to handle requests.")
	}
	if !hasClient {
		add("warn", "No client / entry point on the diagram.")
	}
	if hasClient && apiN > 0 && !clientReaches {
		add("warn", "Client isn’t wired to your API — connect it directly or via a load balancer / CDN.")
	}
	if apiN >= 1 && !hasLB {
		add("warn", "Single API tier — add a load balancer to scale out and remove the single point of failure.")
	}
	if dbN == 0 && !hasStore {
		add("bad", "No datastore — writes have nowhere to persist. Add a database.")
	}
	if dbN > 0 && !dbWired {
		add("warn", "Your API isn’t connected to the database.")
	}
	if !hasCache {
		add("warn", "No cache — reads hit the database directly. Put Redis in front to cut p99 latency.")
	}
	if hasCache && !cacheWired {
		add("warn", "Cache isn’t wired to the API — connect it to absorb reads.")
	}
	if dbN == 1 {
		add("warn", "Single database is a SPOF — add a read replica for failover and read scaling.")
	}
	if !hasQueue {
		add("info", "No async path — a queue would decouple write spikes (optional for this brief).")
	}
	labels := map[string]string{"client": "Client", "cdn": "CDN", "lb": "Load Balancer", "api": "API Server", "cache": "Redis Cache", "db": "Postgres", "queue": "Queue", "store": "Object Store"}
	for _, n := range req.Nodes {
		if degree(n.ID) == 0 {
			add("warn", labels[n.Type]+" isn’t connected to anything.")
		}
	}

	pass := rps >= 50000 && avail >= 99.9 && dbWired && cacheWired && clientReaches
	return sdEvalResp{RPS: rps, P99: p99, Avail: avail, Pass: pass, Findings: findings}
}
