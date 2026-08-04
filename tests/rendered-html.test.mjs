import assert from "node:assert/strict";
import { access, readFile, readdir } from "node:fs/promises";
import test from "node:test";

test("静的ダッシュボードを生成する", async () => {
  const html = await readFile(new URL("../out/index.html", import.meta.url), "utf8");
  assert.match(html, /Shiden Guardian/);
  assert.match(html, /tk_sdn_collator/);
  assert.match(html, /lang="ja"/);
  assert.doesNotMatch(html, /codex-preview|Your site is taking shape/);
});

test("スターターの一時プレビューを残さない", async () => {
  await assert.rejects(access(new URL("../app/_sites-preview/SkeletonPreview.tsx", import.meta.url)));
  const packageJson = await readFile(new URL("../package.json", import.meta.url), "utf8");
  assert.doesNotMatch(packageJson, /react-loading-skeleton|vinext|wrangler/);
});

test("報酬監視画面と固定ウォレットを静的出力へ含める", async () => {
  const chunkDirectory = new URL("../out/_next/static/chunks/", import.meta.url);
  const chunks = (await readdir(chunkDirectory)).filter((name) => name.endsWith(".js"));
  const compiled = (await Promise.all(chunks.map((name) => readFile(new URL(name, chunkDirectory), "utf8")))).join("\n");
  assert.match(compiled, /報酬/);
  assert.match(compiled, /WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN/);
});
