import assert from "node:assert/strict";
import { access, readFile, readdir } from "node:fs/promises";
import test from "node:test";

test("静的ダッシュボードを生成する", async () => {
  const html = await readFile(new URL("../out/index.html", import.meta.url), "utf8");
  assert.match(html, /Shiden Guardian/);
  assert.match(html, /tk_sdn_collator/);
  assert.match(html, /lang="en"/);
  assert.doesNotMatch(html, /codex-preview|Your site is taking shape/);
  await access(new URL("../out/beelink-mini-s.png", import.meta.url));
});

test("スターターの一時プレビューを残さない", async () => {
  await assert.rejects(access(new URL("../app/_sites-preview/SkeletonPreview.tsx", import.meta.url)));
  const packageJson = await readFile(new URL("../package.json", import.meta.url), "utf8");
  assert.doesNotMatch(packageJson, /react-loading-skeleton|vinext|wrangler/);
});

test("日英UI、言語保存、公開ステータス、報酬監視画面を静的出力へ含める", async () => {
  const chunkDirectory = new URL("../out/_next/static/chunks/", import.meta.url);
  const chunks = (await readdir(chunkDirectory)).filter((name) => name.endsWith(".js"));
  const compiled = (await Promise.all(chunks.map((name) => readFile(new URL(name, chunkDirectory), "utf8")))).join("\n");
  assert.match(compiled, /報酬/);
  assert.match(compiled, /Block production rewards/);
  assert.match(compiled, /表示言語/);
  assert.match(compiled, /Display language/);
  assert.match(compiled, /Shiden node status/);
  assert.match(compiled, /Shidenノードの稼働状況/);
  assert.match(compiled, /\/api\/v1\/public\/status/);
  assert.match(compiled, /sg_locale/);
  assert.match(compiled, /WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN/);
  assert.match(compiled, /Collator binary/);
  assert.match(compiled, /コレーターバイナリ/);
  assert.match(compiled, /beelink-mini-s\.png/);
  assert.doesNotMatch(compiled, /All administrative actions are audited/);
  assert.doesNotMatch(compiled, /管理操作はすべて記録され/);
});
