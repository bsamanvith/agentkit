package models

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

// Eval is a model's result on make farm-eval: every scenario passed, or the
// model is not recorded.
type Eval struct {
	// Model is the full name, "<provider>/<id>".
	Model string `json:"model"`
	// Scenarios is how many scenarios ran, all of which passed.
	Scenarios int `json:"scenarios"`
	// Date is the day of the run, YYYY-MM-DD.
	Date string `json:"date"`
	// Note says anything worth knowing about the run.
	Note string `json:"note,omitempty"`
}

//go:embed evaluated.json
var evaluatedJSON []byte

// Evaluated returns the models that passed make farm-eval, from
// evaluated.json (make farm-eval FARM_EVAL_RECORD=1 adds a passing model).
func Evaluated() []Eval {
	var evals []Eval
	if err := json.Unmarshal(evaluatedJSON, &evals); err != nil {
		panic(fmt.Sprintf("models: evaluated.json: %v", err)) // TestEvaluatedFile keeps it valid
	}
	return evals
}

// IsEvaluated reports whether n passed make farm-eval.
func IsEvaluated(n Name) bool {
	return slices.ContainsFunc(Evaluated(), func(e Eval) bool { return e.Model == n.String() })
}

// Record returns evals with e in place of any earlier result of the same
// model, sorted by model, as evaluated.json holds them.
func Record(evals []Eval, e Eval) []Eval {
	out := slices.DeleteFunc(slices.Clone(evals), func(x Eval) bool { return x.Model == e.Model })
	out = append(out, e)
	slices.SortFunc(out, func(a, b Eval) int {
		switch {
		case a.Model < b.Model:
			return -1
		case a.Model > b.Model:
			return 1
		}
		return 0
	})
	return out
}
