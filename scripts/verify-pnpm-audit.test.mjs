import assert from "node:assert/strict";
import test from "node:test";

import { verifyAudit } from "./verify-pnpm-audit.mjs";

const advisoryURL = "https://github.com/advisories/GHSA-8988-4f7v-96qf";

function audit() {
  return {
    advisories: {
      1120821: {
        module_name: "@opentelemetry/core",
        severity: "moderate",
        vulnerable_versions: "<2.8.0",
        patched_versions: ">=2.8.0",
        url: advisoryURL,
        findings: [
          {
            version: "1.30.1",
            paths: [".>@google-cloud/pubsub>@opentelemetry/core"],
          },
        ],
      },
    },
  };
}

function policy() {
  return {
    schemaVersion: "fcp.pnpm-audit-policy/v1",
    exceptions: [
      {
        advisoryUrl: advisoryURL,
        package: "@opentelemetry/core",
        severity: "moderate",
        vulnerableVersions: "<2.8.0",
        patchedVersions: ">=2.8.0",
        findings: [{ version: "1.30.1", path: ".>@google-cloud/pubsub>@opentelemetry/core" }],
        expiresOn: "2026-08-24",
        scope: "compatibility tests only",
        reason: "upstream SDK requires the vulnerable major line",
      },
    ],
  };
}

const reviewDate = new Date("2026-07-24T00:00:00Z");

test("accepts only the exact unexpired advisory", () => {
  assert.deepEqual(verifyAudit(audit(), policy(), reviewDate), {
    ok: true,
    allowedAdvisories: [
      {
        advisoryUrl: advisoryURL,
        package: "@opentelemetry/core",
        severity: "moderate",
        expiresOn: "2026-08-24",
      },
    ],
  });
});

test("rejects unexpected advisories", () => {
  const input = audit();
  input.advisories.extra = {
    module_name: "unexpected",
    severity: "low",
    vulnerable_versions: "*",
    patched_versions: "none",
    url: "https://example.invalid/unexpected",
    findings: [],
  };
  assert.throws(() => verifyAudit(input, policy(), reviewDate), /unexpected pnpm advisories/);
});

test("rejects stale exceptions after the advisory is fixed", () => {
  assert.throws(() => verifyAudit({ advisories: {} }, policy(), reviewDate), /stale pnpm audit exceptions/);
});

test("rejects changed dependency paths", () => {
  const input = audit();
  input.advisories[1120821].findings[0].paths = [".>another-package>@opentelemetry/core"];
  assert.throws(() => verifyAudit(input, policy(), reviewDate), /findings changed/);
});

test("rejects expired exceptions", () => {
  assert.throws(() => verifyAudit(audit(), policy(), new Date("2026-08-25T00:00:00Z")), /expired pnpm audit exception/);
});

test("never permits high or critical exceptions", () => {
  const input = policy();
  input.exceptions[0].severity = "high";
  assert.throws(() => verifyAudit(audit(), input, reviewDate), /cannot be excepted/);
});
