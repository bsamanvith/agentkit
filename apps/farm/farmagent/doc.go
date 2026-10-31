// Package farmagent puts the farm app's data in front of agentkit agents:
// [Tools] are the farm tools any agent can be given (list and read the
// farmer's plots, verify a boundary, convert areas), and [Assistant] is the
// farmer's chat assistant, an agentkit harness over those tools.
//
// The tools own the numbers: an area or a verification comes from package
// farm, never from the model, so the answer is the same however the question
// is asked.
package farmagent
