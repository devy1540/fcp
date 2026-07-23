---
name: fcp-local-cloud
description: Diagnose, configure, inspect, and verify the FCP lightweight AWS/GCP emulator through its machine-readable CLI. Use when an agent needs to start local PODO cloud dependencies, generate emulator environment variables, check FCP health, inspect local resources without exposing payloads, interpret FULL/PARTIAL compatibility, or decide when real AWS/GCP verification is still required.
---

# FCP Local Cloud

Use FCP as a deterministic local integration-test dependency, not as proof of full AWS/GCP parity.

## Locate the CLI

Prefer an installed `fcp` binary. Inside the FCP repository, use `go run ./cmd/fcp` when no binary is available. Append the same subcommands and flags in either form.

## Diagnose first

Run:

```bash
fcp doctor --json
fcp status --json
```

Treat a nonzero exit code or `"ok": false` as a failed check. Do not infer service readiness from the dashboard process badge alone.

Use `--endpoint` and `--gcp-endpoint` when FCP is not on the default loopback ports. Never expose FCP to an untrusted network; it does not validate AWS credentials or SigV4.

## Configure PODO services

Generate environment variables instead of reconstructing them:

```bash
fcp env podo-backend --format shell
fcp env podo-notification --format json
fcp env podo-app --format shell
```

Inspect shell output before sourcing it. Keep generated credentials local and never print their contents.

## Inspect resources

Use the metadata-only resource view:

```bash
fcp resources list --service pubsub --provider gcp --limit 25 --json
fcp resources list --service sqs --query notification --json
```

Do not read FCP state files directly when the CLI can answer the question. The CLI excludes Secret payloads, message bodies, DynamoDB item contents, key material, prompts, and generated text.

## Verify compatibility

Run:

```bash
fcp verify --service gcs --json
fcp verify --strict --json
```

Interpret results precisely:

- `FULL` means the documented scope is regression-tested, not complete cloud parity.
- `PARTIAL` means the result may be sufficient for the PODO path but has named exclusions.
- `SDK` evidence names a client version with a compatibility test.
- `CONTRACT` evidence covers PODO's direct HTTP request/response path.
- Runtime verification only proves the local process exposes the declared contract. Run repository SDK tests when changing protocols or client versions.

Use real AWS/GCP for IAM and signature enforcement, quotas, performance, multi-region behavior, distributed consistency, vendor-specific delivery, and final release confidence.

## Safety

- Keep inspection read-only unless the user explicitly requests a local mutation.
- Do not run reset, purge, delete, or scenario mutation commands without explicit scope.
- Do not expose credentials, Secret values, key material, message bodies, prompts, or generated content.
- Report the exact command, exit code, and failed checks when verification fails.
