package evals

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Run is one run of a scenario.
type Run struct {
	Passed   bool       `json:"passed"`
	Failures []string   `json:"failures,omitempty"`
	Calls    int        `json:"calls"`
	Input    int        `json:"input_tokens"`
	Output   int        `json:"output_tokens"`
	Seconds  float64    `json:"seconds"`
	Detail   Transcript `json:"transcript"`
	// Network lists requests that left the machine; any fails the run.
	Network []string `json:"network,omitempty"`
}

// Result is a scenario's runs.
type Result struct {
	Scenario string `json:"scenario"`
	Behavior string `json:"behavior"`
	// Misleading: the run had the scenario's misleading descriptions.
	Misleading bool  `json:"misleading,omitempty"`
	Runs       []Run `json:"runs"`
}

// PassRate is the share of runs that passed.
func (r Result) PassRate() float64 {
	if len(r.Runs) == 0 {
		return 0
	}
	n := 0
	for _, run := range r.Runs {
		if run.Passed {
			n++
		}
	}
	return float64(n) / float64(len(r.Runs))
}

func (r Result) mean(f func(Run) int) float64 {
	if len(r.Runs) == 0 {
		return 0
	}
	sum := 0
	for _, run := range r.Runs {
		sum += f(run)
	}
	return float64(sum) / float64(len(r.Runs))
}

// Report is one `make eval`: what ran, on which model, and the results.
type Report struct {
	Date       string   `json:"date"`
	Model      string   `json:"model"`
	Runs       int      `json:"runs"`
	Misleading bool     `json:"misleading,omitempty"`
	Results    []Result `json:"results"`
}

// summary is a report's per-scenario line, as it is written in the results
// file next to the runs.
type summary struct {
	Scenario   string  `json:"scenario"`
	PassRate   float64 `json:"pass_rate"`
	Calls      float64 `json:"mean_calls"`
	Input      float64 `json:"mean_input_tokens"`
	Output     float64 `json:"mean_output_tokens"`
	Misleading bool    `json:"misleading,omitempty"`
}

// MarshalJSON adds each scenario's summary.
func (r Report) MarshalJSON() ([]byte, error) {
	type plain Report
	sums := make([]summary, len(r.Results))
	for i, res := range r.Results {
		sums[i] = summarize(res)
	}
	return json.Marshal(struct {
		plain
		Summary []summary `json:"summary"`
	}{plain(r), sums})
}

func summarize(res Result) summary {
	return summary{
		Scenario: res.Scenario, PassRate: res.PassRate(), Misleading: res.Misleading,
		Calls:  res.mean(func(r Run) int { return r.Calls }),
		Input:  res.mean(func(r Run) int { return r.Input }),
		Output: res.mean(func(r Run) int { return r.Output }),
	}
}

// Print writes the report as a table, then the failures with their
// transcripts.
func (r Report) Print(w io.Writer) {
	fmt.Fprintf(w, "eval %s, model %s, %d runs per scenario%s\n\n", r.Date, r.Model, r.Runs, map[bool]string{true: ", misleading descriptions", false: ""}[r.Misleading])
	fmt.Fprintf(w, "%-34s %6s %6s %9s %8s\n", "scenario", "pass", "calls", "input", "output")
	var total, passed int
	for _, res := range r.Results {
		s := summarize(res)
		fmt.Fprintf(w, "%-34s %5.0f%% %6.1f %9.0f %8.0f\n", res.Scenario, 100*s.PassRate, s.Calls, s.Input, s.Output)
		for _, run := range res.Runs {
			total++
			if run.Passed {
				passed++
			}
		}
	}
	if total > 0 {
		fmt.Fprintf(w, "%-34s %5.0f%%\n", "all", 100*float64(passed)/float64(total))
	}
	for _, res := range r.Results {
		for i, run := range res.Runs {
			if run.Passed {
				continue
			}
			fmt.Fprintf(w, "\n--- %s, run %d\n", res.Scenario, i+1)
			for _, f := range run.Failures {
				fmt.Fprintf(w, "  %s\n", f)
			}
			for _, n := range run.Network {
				fmt.Fprintf(w, "  network: %s\n", n)
			}
			for j, c := range run.Detail.Calls {
				fmt.Fprintf(w, "  call %d: %s %s\n", j+1, c.Tool, c.Input)
				fmt.Fprintf(w, "    -> %s%s\n", map[bool]string{true: "error: ", false: ""}[c.IsError], excerpt(c.Result, 300))
			}
			fmt.Fprintf(w, "  answer: %s\n", excerpt(run.Detail.Answer, 600))
		}
	}
}

func excerpt(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Regression is a scenario whose pass rate dropped since the previous report.
type Regression struct {
	Scenario       string
	Before, After  float64
	PreviousReport string
}

// Compare lists the scenarios whose pass rate is lower in cur than in prev.
// Scenarios run with misleading descriptions are compared only with those.
func Compare(prev, cur Report) []Regression {
	before := map[string]float64{}
	for _, r := range prev.Results {
		if r.Misleading == cur.Misleading {
			before[r.Scenario] = r.PassRate()
		}
	}
	var out []Regression
	for _, r := range cur.Results {
		if b, ok := before[r.Scenario]; ok && r.PassRate() < b {
			out = append(out, Regression{Scenario: r.Scenario, Before: b, After: r.PassRate(), PreviousReport: prev.Date})
		}
	}
	return out
}

// WriteReport writes the report to dir/<date>.json (with a -2, -3, ...
// suffix if that exists) and returns the path.
func WriteReport(dir string, r Report) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	name := r.Date
	if r.Misleading {
		name += "-misleading"
	}
	path := filepath.Join(dir, name+".json")
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.json", name, i))
	}
	return path, os.WriteFile(path, append(data, '\n'), 0o644)
}

// LatestReport reads the newest report in dir of the same kind (misleading
// or not), by file name; ok is false if there is none.
func LatestReport(dir string, misleading bool) (r Report, path string, ok bool, err error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return Report{}, "", false, err
	}
	sort.Slice(files, func(i, j int) bool { return reportOrder(files[i]) < reportOrder(files[j]) })
	for _, f := range slices.Backward(files) {
		if strings.Contains(filepath.Base(f), "-misleading") != misleading {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return Report{}, "", false, err
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return Report{}, "", false, fmt.Errorf("%s: %w", f, err)
		}
		return r, f, true, nil
	}
	return Report{}, "", false, nil
}

// reportOrder sorts 2026-10-22.json before 2026-10-22-2.json before
// 2026-10-22-10.json (and the same with -misleading after the date).
func reportOrder(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	if len(base) < 10 {
		return base
	}
	n := strings.TrimPrefix(strings.TrimPrefix(base[10:], "-misleading"), "-")
	if n == "" {
		n = "1"
	}
	return fmt.Sprintf("%s-%06s", base[:10], n)
}
