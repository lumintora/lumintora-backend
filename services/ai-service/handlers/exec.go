package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"lumintora/pkg/httputil"
)

// ExecHandler is a language-agnostic code-execution gateway. JavaScript runs
// client-side in a Web Worker for instant feedback; this gateway handles the
// compiled / heavier languages (python3, java, c, c++) by proxying to a real
// sandboxed runner.
type ExecHandler struct {
	apiURL string
	client *http.Client

	mu        sync.RWMutex
	compilers map[string]string
}

func NewExecHandler() *ExecHandler {
	return &ExecHandler{
		apiURL:    strings.TrimRight(getEnv("CODE_EXEC_URL", "https://wandbox.org"), "/"),
		client:    &http.Client{Timeout: 60 * time.Second},
		compilers: map[string]string{},
	}
}

var langToWandbox = map[string]string{
	"python3": "Python", "python": "Python", "py": "Python",
	"java": "Java",
	"c":    "C",
	"cpp":  "C++", "c++": "C++",
}

var javaPublicClass = regexp.MustCompile(`public\s+((?:final\s+|abstract\s+)*(?:class|interface|enum)\b)`)

func (h *ExecHandler) Execute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Language string `json:"language"`
		Source   string `json:"source"`
		Stdin    string `json:"stdin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Source) == "" {
		httputil.Error(w, "source is empty", http.StatusBadRequest)
		return
	}
	if len(req.Source) > 50_000 {
		httputil.Error(w, "source too large (50KB max)", http.StatusBadRequest)
		return
	}

	key := strings.ToLower(strings.TrimSpace(req.Language))
	wbLang, ok := langToWandbox[key]
	if !ok {
		httputil.Error(w, "unsupported language", http.StatusBadRequest)
		return
	}

	compiler, err := h.resolveCompiler(key, wbLang)
	if err != nil {
		httputil.Error(w, "code runner unavailable, please try again", http.StatusServiceUnavailable)
		return
	}

	source := req.Source
	if wbLang == "Java" {
		source = javaPublicClass.ReplaceAllString(source, "$1")
	}

	payload := map[string]interface{}{
		"code":     source,
		"compiler": compiler,
		"stdin":    req.Stdin,
		"save":     false,
	}
	body, _ := json.Marshal(payload)

	resp, err := h.client.Post(h.apiURL+"/api/compile.json", "application/json", bytes.NewReader(body))
	if err != nil {
		httputil.Error(w, "code runner unavailable, please try again", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusTooManyRequests {
		httputil.Error(w, "the shared code runner is busy — try again in a moment", http.StatusTooManyRequests)
		return
	}
	if resp.StatusCode >= 400 {
		httputil.Error(w, "code runner error, please try again", http.StatusServiceUnavailable)
		return
	}

	var wr struct {
		Status          string `json:"status"`
		Signal          string `json:"signal"`
		ProgramOutput   string `json:"program_output"`
		ProgramError    string `json:"program_error"`
		CompilerOutput  string `json:"compiler_output"`
		CompilerError   string `json:"compiler_error"`
		CompilerMessage string `json:"compiler_message"`
	}
	if err := json.Unmarshal(data, &wr); err != nil {
		httputil.Error(w, "could not read runner output", http.StatusServiceUnavailable)
		return
	}

	compileErr := strings.TrimSpace(wr.CompilerError)
	compiled := compileErr == ""
	exitCode, _ := strconv.Atoi(strings.TrimSpace(wr.Status))
	timedOut := wr.Signal != "" && (strings.Contains(wr.Signal, "KILL") || strings.Contains(wr.Signal, "XCPU"))

	stderr := strings.TrimRight(wr.ProgramError, "\n")
	if !compiled {
		stderr = compileErr
	} else if timedOut && stderr == "" {
		stderr = "Execution timed out (possible infinite loop or too slow)."
	}

	httputil.OK(w, map[string]interface{}{
		"language":  req.Language,
		"compiler":  compiler,
		"stdout":    strings.TrimRight(wr.ProgramOutput, "\n"),
		"stderr":    stderr,
		"exit_code": exitCode,
		"timed_out": timedOut,
		"compiled":  compiled,
		"ok":        compiled && exitCode == 0 && !timedOut,
	})
}

func (h *ExecHandler) resolveCompiler(key, wbLang string) (string, error) {
	h.mu.RLock()
	c, ok := h.compilers[key]
	h.mu.RUnlock()
	if ok {
		return c, nil
	}

	resp, err := h.client.Get(h.apiURL + "/api/list.json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var list []struct {
		Name     string `json:"name"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return "", err
	}

	var candidates []string
	for _, item := range list {
		if item.Language == wbLang {
			candidates = append(candidates, item.Name)
		}
	}
	best := bestCompiler(candidates)
	if best == "" {
		return "", fmt.Errorf("no compiler for %s", wbLang)
	}

	h.mu.Lock()
	h.compilers[key] = best
	h.mu.Unlock()
	return best, nil
}

func bestCompiler(names []string) string {
	for _, n := range names {
		if strings.Contains(n, "head") || strings.Contains(n, "-2.") {
			continue
		}
		return n
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}
