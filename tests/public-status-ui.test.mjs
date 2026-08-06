import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("公開ステータス取得は認証情報を送らずログイン画面だけで更新する", async () => {
  const source = await readFile(new URL("../app/page.tsx", import.meta.url), "utf8");
  assert.match(source, /fetch\("\/api\/v1\/public\/status"/);
  assert.match(source, /credentials:\s*"omit"/);
  assert.match(source, /window\.setInterval\(load,\s*30_000\)/);
  assert.match(source, /needsBootstrap === null/);

  const login = source.indexOf("function Login");
  const publicCardUse = source.indexOf("<PublicStatusCard />", login);
  const bootstrap = source.indexOf("function Bootstrap", login);
  assert.ok(login >= 0 && publicCardUse > login && publicCardUse < bootstrap, "public status card must only be mounted by the normal login view");
});

test("公開カードはpeerや管理情報を表示せずモバイルではログイン後段に積む", async () => {
  const [source, css] = await Promise.all([
    readFile(new URL("../app/page.tsx", import.meta.url), "utf8"),
    readFile(new URL("../app/globals.css", import.meta.url), "utf8"),
  ]);
  const start = source.indexOf("function PublicStatusCard");
  const end = source.indexOf("function Bootstrap", start);
  const card = source.slice(start, end);
  assert.doesNotMatch(card, /peer|wallet|incident|automation|cpu|memory|disk/i);
  assert.match(card, /finalized_block/);
  assert.match(css, /grid-template-areas:\s*"status login"/);
  assert.match(css, /@media \(max-width: 820px\)[\s\S]*grid-template-areas:\s*"login" "status"/);
});
