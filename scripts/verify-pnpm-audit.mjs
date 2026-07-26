#!/usr/bin/env node

import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const policySchema = "fcp.pnpm-audit-policy/v1";
const forbiddenExceptionSeverities = new Set(["high", "critical"]);

export function verifyAudit(audit, policy, today = new Date()) {
  if (!audit || typeof audit !== "object" || !audit.advisories || typeof audit.advisories !== "object") {
    throw new Error("pnpm audit output does not contain an advisories object");
  }
  if (policy?.schemaVersion !== policySchema || !Array.isArray(policy.exceptions)) {
    throw new Error(`audit policy must use ${policySchema}`);
  }

  const policyByURL = new Map();
  for (const exception of policy.exceptions) {
    validateException(exception, today);
    if (policyByURL.has(exception.advisoryUrl)) {
      throw new Error(`duplicate audit exception: ${exception.advisoryUrl}`);
    }
    policyByURL.set(exception.advisoryUrl, exception);
  }

  const advisories = Object.values(audit.advisories);
  const actualByURL = new Map();
  for (const advisory of advisories) {
    if (!advisory?.url || actualByURL.has(advisory.url)) {
      throw new Error(`pnpm audit returned a missing or duplicate advisory URL: ${advisory?.url ?? "<missing>"}`);
    }
    actualByURL.set(advisory.url, advisory);
  }

  const unexpected = [...actualByURL.keys()].filter((url) => !policyByURL.has(url));
  if (unexpected.length > 0) {
    throw new Error(`unexpected pnpm advisories: ${unexpected.sort().join(", ")}`);
  }
  const stale = [...policyByURL.keys()].filter((url) => !actualByURL.has(url));
  if (stale.length > 0) {
    throw new Error(`stale pnpm audit exceptions must be removed: ${stale.sort().join(", ")}`);
  }

  const allowed = [];
  for (const [url, exception] of policyByURL) {
    const advisory = actualByURL.get(url);
    requireEqual(url, "package", advisory.module_name, exception.package);
    requireEqual(url, "severity", advisory.severity, exception.severity);
    requireEqual(url, "vulnerableVersions", advisory.vulnerable_versions, exception.vulnerableVersions);
    requireEqual(url, "patchedVersions", advisory.patched_versions, exception.patchedVersions);

    const actualFindings = normalizeActualFindings(advisory);
    const expectedFindings = normalizeExpectedFindings(exception);
    requireEqual(url, "findings", JSON.stringify(actualFindings), JSON.stringify(expectedFindings));
    allowed.push({
      advisoryUrl: url,
      package: exception.package,
      severity: exception.severity,
      expiresOn: exception.expiresOn,
    });
  }

  return { ok: true, allowedAdvisories: allowed };
}

function validateException(exception, today) {
  for (const field of [
    "advisoryUrl",
    "package",
    "severity",
    "vulnerableVersions",
    "patchedVersions",
    "expiresOn",
    "scope",
    "reason",
  ]) {
    if (typeof exception?.[field] !== "string" || exception[field].trim() === "") {
      throw new Error(`audit exception field ${field} is required`);
    }
  }
  if (!Array.isArray(exception.findings) || exception.findings.length === 0) {
    throw new Error(`audit exception findings are required for ${exception.advisoryUrl}`);
  }
  if (forbiddenExceptionSeverities.has(exception.severity.toLowerCase())) {
    throw new Error(`high or critical advisory cannot be excepted: ${exception.advisoryUrl}`);
  }
  if (!/^\d{4}-\d{2}-\d{2}$/.test(exception.expiresOn)) {
    throw new Error(`invalid exception expiry for ${exception.advisoryUrl}: ${exception.expiresOn}`);
  }
  const expiry = new Date(`${exception.expiresOn}T23:59:59.999Z`);
  if (Number.isNaN(expiry.getTime()) || today.getTime() > expiry.getTime()) {
    throw new Error(`expired pnpm audit exception: ${exception.advisoryUrl} (${exception.expiresOn})`);
  }
}

function normalizeActualFindings(advisory) {
  if (!Array.isArray(advisory.findings)) {
    return [];
  }
  const findings = [];
  for (const finding of advisory.findings) {
    for (const dependencyPath of finding.paths ?? []) {
      findings.push({ version: String(finding.version), path: String(dependencyPath) });
    }
  }
  return sortFindings(findings);
}

function normalizeExpectedFindings(exception) {
  return sortFindings(
    exception.findings.map((finding) => ({
      version: String(finding.version),
      path: String(finding.path),
    })),
  );
}

function sortFindings(findings) {
  return findings.sort((left, right) =>
    left.version === right.version ? left.path.localeCompare(right.path) : left.version.localeCompare(right.version),
  );
}

function requireEqual(url, field, actual, expected) {
  if (actual !== expected) {
    throw new Error(`${url} ${field} changed: actual=${actual} expected=${expected}`);
  }
}

async function main() {
  const [auditPath, policyPath] = process.argv.slice(2);
  if (!auditPath || !policyPath) {
    throw new Error("usage: verify-pnpm-audit.mjs AUDIT_JSON POLICY_JSON");
  }
  const [auditRaw, policyRaw] = await Promise.all([readFile(auditPath, "utf8"), readFile(policyPath, "utf8")]);
  const result = verifyAudit(JSON.parse(auditRaw), JSON.parse(policyRaw));
  process.stdout.write(`${JSON.stringify(result)}\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    process.stderr.write(`pnpm audit policy failed: ${error.message}\n`);
    process.exitCode = 1;
  });
}
