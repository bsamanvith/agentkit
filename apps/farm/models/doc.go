// Package models is the farm app's model lane (T142): which language models
// the farm assistant and its skills may run on, in what order, and whether
// each has passed the farm's own evaluation (make farm-eval).
//
// A lane is a list of model names, "<provider>/<id>", tried in order:
//
//	anthropic/claude-haiku-5-5,gemini/gemini-3.5-flash
//
// A bare provider ("anthropic") means its default model. The providers are
// a table ([Providers]): Anthropic (Claude), OpenAI, OpenRouter (many
// cheaper and some free models behind one OpenAI-compatible API, ids such
// as "openrouter/meta-llama/llama-3.3-70b-instruct:free") and Gemini. Each
// reads its key from the environment; a model whose key is not set is left
// out of the lane, as is one that has not passed make farm-eval
// ([Evaluated], recorded in evaluated.json) unless the lane is built with
// [AllowUnevaluated].
//
// The [Lane] is itself an llm.Model. A request goes to the first model that
// is up; when it fails before saying anything with an error another model
// could avoid (rate limited, overloaded, a bad or exhausted key), the next
// model takes the same request, and the failed one rests for a while
// ([RestTransient], [RestAuth]) so the next requests go straight to a
// standby. An error after the reply has started, or one the request itself
// causes (invalid, too long), is returned as it is.
//
// Adding a provider is a row in [Providers] and its build function; the
// farm binary and the eval read the same table.
package models
