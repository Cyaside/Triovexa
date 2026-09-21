# Reasoning evaluation

`cases.json` is the fixed 30-case set used to compare deterministic heuristics with a configured OpenAI-compatible model. It contains ten worker stalls, eight timeout-after-deploy cases, six error-rate spikes, and six ambiguous cases that should produce no remediation.

The score treats a case as accurate when the expected valid action is present, or when an ambiguous case produces no action. Evidence grounding requires every proposed action to reference evidence IDs from the same case. Invalid catalog proposals and provider fallbacks are counted separately. Latency covers triage and remediation together; token totals are reported only when the endpoint returns Chat Completions usage.

Run the deterministic baseline:

```powershell
go run ./cmd/evaluator
```

Run a provider three times per case:

```powershell
$env:TRIOVEXA_EVAL_API_KEY = "..."
go run ./cmd/evaluator -mode provider -provider openai-compatible `
  -base-url https://api.example.com/v1 -model example-model -repeats 3 `
  -out evaluation/results/example-model.json
```

Supply the endpoint's `-base-url` and `-model`. Use the same cases, repeats, and prompt version when comparing reports.

The committed heuristic result is a regression baseline for these designed cases, not an estimate of real-world incident accuracy. Provider reports are not checked in until they have been run against a named model with a real credential.
