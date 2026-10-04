# Model admission and budgets

Triovexa accounts model requests across triage, remediation, connection checks, and code investigation through a persistent campaign. Configure the server and host investigation worker with the same `AI_BUDGET_CONFIG_PATH`. Store the configuration outside version control; provider credentials remain in the configured server credential store.

Start with [the configuration example](model-budget.example.json). It is intentionally unable to authorize paid requests: the model and tariff are placeholders, and all billing verification flags are false. Replace these values only after checking the configured provider's billing contract. Unknown pricing, an unbounded billable output, or an unverified input bound prevents dispatch.

## Configuration fields

| Field | Meaning |
| --- | --- |
| `campaign.id` | Stable identifier for the cumulative accounting campaign. Reuse it after a restart. |
| `campaign.profile` | `internal`, `final-smoke`, or `offline-fixture`. |
| `campaign.offline` | Must be true for `offline-fixture`; external model requests are rejected. |
| `campaign.max_spend_micro_usd` | Cumulative spend ceiling in integer micro-US dollars. `1000000` means US$1. |
| `campaign.max_input_tokens` | Cumulative admitted input bound across requests in the campaign. |
| `campaign.max_requests` | Maximum admitted request count, including requests from different components. |
| `pricing.version` | Identifier for the verified tariff snapshot. |
| `pricing.provider` / `pricing.model` | Must match the configured provider and exact model identifier. |
| `pricing.verified` | Confirms that the tariff is known and was checked. |
| `pricing.input_bound_verified` | Confirms that the conservative input bound is valid under the provider's tokenization and billing contract. |
| `pricing.billable_output_bound` | Confirms that `max_output_tokens` bounds every billable output category. |
| `pricing.input_micro_usd_per_million` | Input price in micro-US dollars per million tokens. |
| `pricing.output_micro_usd_per_million` | Output price in the same units. |
| `pricing.fixed_request_micro_usd` | Additional fixed charge per request, if applicable. |
| `config_version` | Identifier for the immutable request configuration. |
| `max_input_tokens` | Per-request conservative input bound. |
| `max_output_tokens` | Per-request output limit sent to the provider. |
| `repair_candidate_limit` | Defaults to one candidate. Only the `internal` profile can explicitly authorize two or three candidates. |

For conversion, a tariff of US$1 per million tokens is `1000000` in either per-million price field. This is a unit example, not a provider tariff. Use a conservative verified rate for all billable tokens; cached and reasoning details are subsets of the reported totals and are not added twice. A provider that bills uncapped hidden reasoning cannot satisfy the billable-output bound by setting `max_output_tokens` alone.

The input bound covers the final serialized request, including system messages, tool schemas, and transcript. A characters-per-token estimate is not sufficient verification. Check the provider's documented contract before enabling either bounding flag.

The `final-smoke` profile additionally limits the campaign to US$0.20, six requests, and 12000 cumulative admitted input tokens; per-request input and output are capped at 6000 and 1500 respectively. It permits one repair candidate. These are ceilings, not a promise that a complete investigation fits within them. Admission can stop earlier when the estimated request would exceed any remaining bound.

## Restart and uncertain requests

Admission reserves the conservative cost before dispatch and records actual usage when a valid response is received. The ledger and sealed response receipts survive a restart. A completed request with the same identity and payload can replay its receipt without another paid request. A different payload, model, tariff, or configuration cannot reuse that identity.

Missing or invalid usage, a process crash after dispatch, or an uncertain provider response keeps the reservation held and blocks later paid requests in the campaign. Inspect the provider's usage records and the persisted request before deciding how to proceed; there is no automatic refund or retry. Do not delete accounting rows or change the campaign identifier to work around an unresolved request.

An additional candidate is an explicit new proposal within the same campaign and model-request budget. Go first restores the failed candidate to its approved private base. A repeated patch, failed restoration, unavailable test, or exhausted candidate budget ends the investigation. The system does not silently switch to a heuristic or another provider.
