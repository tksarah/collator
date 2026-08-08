import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

async function catalog(name) {
  return JSON.parse(await readFile(new URL(`../app/locales/${name}.json`, import.meta.url), "utf8"));
}

function placeholders(value) {
  return [...value.matchAll(/\{([A-Za-z0-9_]+)\}/g)].map((match) => match[1]).sort();
}

test("英語と日本語のカタログキーと補間変数が一致する", async () => {
  const [en, ja] = await Promise.all([catalog("en"), catalog("ja")]);
  const enKeys = Object.keys(en).sort();
  const jaKeys = Object.keys(ja).sort();

  assert.deepEqual(jaKeys, enKeys);
  assert.ok(enKeys.length > 150, "expected the complete UI catalog");

  for (const key of enKeys) {
    assert.equal(typeof en[key], "string", `en.${key} must be a string`);
    assert.equal(typeof ja[key], "string", `ja.${key} must be a string`);
    assert.ok(en[key].trim(), `en.${key} must not be empty`);
    assert.ok(ja[key].trim(), `ja.${key} must not be empty`);
    assert.deepEqual(placeholders(ja[key]), placeholders(en[key]), `${key} placeholders must match`);
  }
});

test("既知インシデントと基本画面の翻訳を含む", async () => {
  const [en, ja] = await Promise.all([catalog("en"), catalog("ja")]);
  for (const key of [
    "auth.title",
    "publicStatus.title",
    "publicStatus.overall.operational",
    "tabs.settings",
    "incident.service-inactive",
    "incident.reward-silence.critical",
    "restart.accepted",
    "rewards.recoveryRequired",
    "rewardRecovery.accepted",
  ]) {
    assert.ok(en[key], `missing English ${key}`);
    assert.ok(ja[key], `missing Japanese ${key}`);
  }
});
