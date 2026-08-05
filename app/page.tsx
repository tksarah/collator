"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

type Tab = "overview" | "metrics" | "rewards" | "logs" | "incidents" | "maintenance" | "settings";
type RewardOverview = {
  address: string; status: string; monitoring_started_at?: string; active_session: boolean; validator_count: number;
  finalized_block: number; last_scanned_block: number; last_authored_block: number; last_reward_at?: string;
  seconds_since_reward: number; blocks_since_authored: number; kick_blocks_remaining: number; wallet_free_planck: string;
  last_reward_planck: string; reward_24h_planck: string; reward_24h_count: number; reward_total_planck: string;
  reward_total_count: number; spec_version: number; schema_ok: boolean; quorum: number; inactive_confirmations?: number; sources: Record<string, string>; gap?: string;
};
type RewardObservation = { block_number: number; block_hash: string; authored_at: string; expected_planck: string; credited_planck: string; verification: string; source_count: number };
type RewardDaily = { day: string; amount_planck: string; count: number };
type RewardPage = { summary: RewardOverview; items: RewardObservation[]; daily?: RewardDaily[]; next_before_block?: number };
type Overview = {
  node: { name: string; service_state: string; version: string; uptime_seconds: number; restart_count: number };
  chain: { local_finalized: number; local_best: number; external_height: number; lag: number; peers: number; status: string };
  host: { cpu_percent: number; memory_percent: number; disk_percent: number; temperature_c: number };
  automation: { mode: string; enabled: boolean; host_locked: boolean; eligible_at: string };
  rewards: RewardOverview;
  incidents: Incident[];
  updated_at: string;
};
type Incident = { id: string; severity: string; title: string; status: string; opened_at: string; diagnosis?: string };
type LogLine = { cursor: string; timestamp: string; priority: string; message: string };
type Audit = {
  id: string;
  action: string;
  actor: string;
  created_at: string;
  result: string;
  details?: { error?: unknown; job_id?: unknown };
};
type TimePoint = { timestamp: number; value: number };
type PrometheusSeries = { metric?: Record<string, string>; values?: [number, string][] };
type PrometheusResponse = { data?: { result?: PrometheusSeries[] } };
type LoadedSeries = { id: string; label: string; metric: Record<string, string>; points: TimePoint[] };
type PanelState = { series: LoadedSeries[]; loading: boolean; error: string };
type ChartSeries = LoadedSeries & { color: string; curve?: "linear" | "step"; fill?: boolean };
type ChartThreshold = { value: number; label: string; color: string; band?: "above" | "below" };

const tabs: { id: Tab; label: string; mark: string }[] = [
  { id: "overview", label: "概要", mark: "01" },
  { id: "metrics", label: "メトリクス", mark: "02" },
  { id: "rewards", label: "報酬", mark: "03" },
  { id: "logs", label: "ログ", mark: "04" },
  { id: "incidents", label: "インシデント", mark: "05" },
  { id: "maintenance", label: "メンテナンス", mark: "06" },
  { id: "settings", label: "設定・監査", mark: "07" },
];

const demoOverview: Overview = {
  node: { name: "tk_sdn_collator", service_state: "active", version: "v5.48.1", uptime_seconds: 2_486_420, restart_count: 0 },
  chain: { local_finalized: 9_842_716, local_best: 9_842_719, external_height: 9_842_719, lag: 0, peers: 47, status: "healthy" },
  host: { cpu_percent: 32, memory_percent: 61, disk_percent: 54, temperature_c: 47 },
  automation: { mode: "observe_only", enabled: false, host_locked: false, eligible_at: "2026-08-18T12:00:00Z" },
  rewards: { address: "WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN", status: "healthy", monitoring_started_at: "2026-08-05T00:00:00Z", active_session: true, validator_count: 12, finalized_block: 9_842_716, last_scanned_block: 9_842_716, last_authored_block: 9_842_704, last_reward_at: new Date(Date.now()-72_000).toISOString(), seconds_since_reward: 72, blocks_since_authored: 12, kick_blocks_remaining: 1188, wallet_free_planck: "6278626775264166000000", last_reward_planck: "241000000000000000", reward_24h_planck: "289200000000000000000", reward_24h_count: 1200, reward_total_planck: "867600000000000000000", reward_total_count: 3600, spec_version: 2300, schema_ok: true, quorum: 3, sources: { local: "ok", external_1: "ok", external_2: "ok" } },
  incidents: [
    { id: "demo-1", severity: "warning", title: "Relay peer数が一時的に低下", status: "resolved", opened_at: "2026-08-03T03:41:00Z", diagnosis: "90秒以内に自然回復しました。操作は行っていません。" },
  ],
  updated_at: new Date().toISOString(),
};

const chartValues = [34, 38, 36, 44, 41, 52, 48, 45, 57, 54, 61, 58, 64, 59, 62, 67, 61, 57, 53, 49, 46, 51, 45, 43, 47, 39, 36, 42, 35, 32];
const rangeSeconds: Record<string, number> = { "30m": 1800, "1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000 };
const demoModeEnabled = process.env.NEXT_PUBLIC_DEMO_MODE === "true";

function fmtNumber(value: number) {
  return new Intl.NumberFormat("ja-JP").format(value);
}

function fmtTime(value: string) {
  return new Intl.DateTimeFormat("ja-JP", { timeZone: "Asia/Tokyo", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(value));
}

function fmtUptime(seconds: number) {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  return `${days}日 ${hours}時間`;
}

function fmtPlanck(value?: string, digits = 6) {
  try {
    const raw = BigInt(value || "0");
    const base = BigInt(10) ** BigInt(18);
    const whole = raw / base;
    const fraction = (raw % base).toString().padStart(18, "0").slice(0, digits).replace(/0+$/, "");
    return `${new Intl.NumberFormat("ja-JP").format(whole)}${fraction ? `.${fraction}` : ""} SDN`;
  } catch { return "—"; }
}

function fmtDuration(seconds: number) {
  if (seconds < 60) return `${seconds}秒`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}分`;
  return `${Math.floor(seconds / 3600)}時間 ${Math.floor((seconds % 3600) / 60)}分`;
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const csrf = sessionStorage.getItem("sg_csrf");
  const response = await fetch(`/api/v1${path}`, {
    credentials: "include",
    ...init,
    headers: { "Content-Type": "application/json", ...(csrf ? { "X-CSRF-Token": csrf } : {}), ...(init?.headers || {}) },
  });
  if (response.status === 401) throw new Error("unauthorized");
  if (response.status === 429) throw new Error("rate_limited");
  if (!response.ok) throw new Error((await response.text()) || `HTTP ${response.status}`);
  const nextCsrf = response.headers.get("X-CSRF-Token");
  if (nextCsrf) sessionStorage.setItem("sg_csrf", nextCsrf);
  return response.json() as Promise<T>;
}

function normalizePoints(values: [number, string][] = []): TimePoint[] {
  const byTimestamp = new Map<number, number>();
  for (const [timestamp, rawValue] of values) {
    const value = Number(rawValue);
    if (Number.isFinite(timestamp) && Number.isFinite(value)) byTimestamp.set(timestamp, value);
  }
  return [...byTimestamp.entries()]
    .sort(([left], [right]) => left - right)
    .map(([timestamp, value]) => ({ timestamp, value }));
}

function makeDemoPoints(values: number[], range: string): TimePoint[] {
  const now = Math.floor(Date.now() / 1000);
  const duration = rangeSeconds[range] ?? rangeSeconds["24h"];
  const step = values.length > 1 ? duration / (values.length - 1) : duration;
  return values.map((value, index) => ({ timestamp: now - duration + index * step, value }));
}

function parsePrometheus(response: PrometheusResponse): LoadedSeries[] {
  return (response.data?.result ?? []).map((item, index) => {
    const metric = item.metric ?? {};
    const id = metric.__name__ ?? metric.job ?? `series-${index}`;
    return { id, label: id, metric, points: normalizePoints(item.values) };
  }).filter((item) => item.points.length > 0);
}

function usePrometheusPanel(panel: string, range: string, demo: boolean, demoSeries: LoadedSeries[]): PanelState {
  const [state, setState] = useState<PanelState>({ series: [], loading: true, error: "" });
  useEffect(() => {
    if (demo) return;

    let stopped = false;
    let current: AbortController | null = null;
    async function load() {
      current?.abort();
      current = new AbortController();
      try {
        const response = await api<PrometheusResponse>(`/metrics/${panel}?range=${range}`, { signal: current.signal });
        if (!stopped) setState({ series: parsePrometheus(response), loading: false, error: "" });
      } catch (error) {
        if (error instanceof DOMException && error.name === "AbortError") return;
        if (!stopped) setState((previous) => ({ ...previous, loading: false, error: "履歴データを取得できませんでした。15秒後に再試行します。" }));
      }
    }

    void load();
    const timer = window.setInterval(load, 15_000);
    return () => {
      stopped = true;
      current?.abort();
      window.clearInterval(timer);
    };
  }, [panel, range, demo, demoSeries]);
  return demo ? { series: demoSeries, loading: false, error: "" } : state;
}

function seriesValues(series: LoadedSeries[] | ChartSeries[]) {
  return series.flatMap((item) => item.points.map((point) => point.value));
}

function average(values: number[]) {
  return values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : 0;
}

function percentile(values: number[], ratio: number) {
  if (!values.length) return 0;
  const sorted = [...values].sort((left, right) => left - right);
  return sorted[Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * ratio) - 1))];
}

function latestValue(series: LoadedSeries[]) {
  return series[0]?.points.at(-1)?.value;
}

export default function Dashboard() {
  const [tab, setTab] = useState<Tab>("overview");
  const [overview, setOverview] = useState<Overview>(demoOverview);
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [demo, setDemo] = useState(false);
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [audits, setAudits] = useState<Audit[]>([]);
  const [menuOpen, setMenuOpen] = useState(false);
  const [restartOpen, setRestartOpen] = useState(false);
  const [automationOpen, setAutomationOpen] = useState(false);
  const [notice, setNotice] = useState("");

  const refresh = useCallback(async () => {
    try {
      const data = await api<Overview>("/overview");
      setOverview(data);
      setAuthenticated(true);
      setDemo(false);
    } catch (error) {
      if (error instanceof Error && error.message === "unauthorized") {
        setAuthenticated(false);
        return;
      }
      if (demoModeEnabled) {
        setOverview({ ...demoOverview, updated_at: new Date().toISOString() });
        setAuthenticated(true);
        setDemo(true);
      } else {
        setAuthenticated(false);
      }
    }
  }, []);

  const refreshAudits = useCallback(async () => {
    if (demo) return;
    try {
      const data = await api<{ events: Audit[] }>("/audit?limit=30");
      setAudits(data.events);
    } catch {
      setAudits([]);
    }
  }, [demo]);

  useEffect(() => {
    const initial = window.setTimeout(refresh, 0);
    const timer = window.setInterval(refresh, 15_000);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(timer);
    };
  }, [refresh]);

  useEffect(() => {
    if (tab === "logs" && !demo) api<{ lines: LogLine[] }>("/logs?limit=200").then((x) => setLogs(x.lines)).catch(() => setLogs([]));
  }, [tab, demo]);

  useEffect(() => {
    if (tab !== "settings" || demo) return;
    const initial = window.setTimeout(refreshAudits, 0);
    const timer = window.setInterval(refreshAudits, 5_000);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(timer);
    };
  }, [tab, demo, refreshAudits]);

  if (authenticated === null) return <main className="login-shell"><section className="login-card"><div className="brand-orbit large"><span /></div><p>Shiden Guardianへ安全に接続しています…</p></section></main>;
  if (authenticated === false) return <Login onSuccess={refresh} />;

  return (
    <main className="app-shell">
      <aside className={menuOpen ? "sidebar open" : "sidebar"}>
        <div className="brand">
          <div className="brand-orbit"><span /></div>
          <div><strong>SHIDEN</strong><small>GUARDIAN</small></div>
        </div>
        <div className="network-badge"><span className="pulse-dot" />KUSAMA · SHIDEN</div>
        <nav aria-label="メインナビゲーション">
          {tabs.map((item) => (
            <button key={item.id} className={tab === item.id ? "nav-item active" : "nav-item"} onClick={() => { setTab(item.id); setMenuOpen(false); }}>
              <span className="nav-mark">{item.mark}</span><span>{item.label}</span>
            </button>
          ))}
        </nav>
        <div className="sidebar-foot">
          <p>NODE</p><strong>{overview.node.name}</strong>
          <a href="https://telemetry.polkadot.io/#list/0xf1cf9022c7ebb34b162d5b5e34e705a5a740b2d0ecc1009fb89023e62a488108" target="_blank" rel="noreferrer">Telemetryを開く ↗</a>
        </div>
      </aside>

      <section className="workspace">
        <header className="topbar">
          <button className="menu-button" aria-label="メニュー" onClick={() => setMenuOpen((v) => !v)}>☰</button>
          <div>
            <span className="eyebrow">OPERATIONS / {tabs.find((x) => x.id === tab)?.label}</span>
            <h1>{tabs.find((x) => x.id === tab)?.label}</h1>
          </div>
          <div className="topbar-actions">
            {demo && <span className="demo-label">プレビュー</span>}
            <span className="last-update">更新 {fmtTime(overview.updated_at)}</span>
            <button className="refresh-button" onClick={refresh} aria-label="更新">↻</button>
            <button className="small-button" onClick={async () => { try { await api("/auth/logout", { method: "POST", body: "{}" }); } finally { sessionStorage.removeItem("sg_csrf"); setAuthenticated(false); } }}>ログアウト</button>
          </div>
        </header>

        {notice && <div className="notice" role="status">{notice}<button onClick={() => setNotice("")}>×</button></div>}
        {tab === "overview" && <OverviewTab data={overview} demo={demo} onRestart={() => setRestartOpen(true)} />}
        {tab === "metrics" && <MetricsTab data={overview} demo={demo} />}
        {tab === "rewards" && <RewardsTab overview={overview.rewards} demo={demo} />}
        {tab === "logs" && <LogsTab lines={logs} demo={demo} />}
        {tab === "incidents" && <IncidentsTab incidents={overview.incidents} demo={demo} onDiagnose={async () => { if (!demo) await api("/diagnoses", { method: "POST", body: "{}" }); setNotice("AI診断をキューへ登録しました。完了時に監査履歴へ記録します。" ); }} />}
        {tab === "maintenance" && <MaintenanceTab data={overview} onRestart={() => setRestartOpen(true)} />}
        {tab === "settings" && <SettingsTab data={overview} audits={audits} demo={demo} setNotice={setNotice} onAuditRefresh={refreshAudits} onAutomation={() => setAutomationOpen(true)} />}
      </section>
      {restartOpen && <RestartDialog demo={demo} onClose={() => setRestartOpen(false)} onDone={(message) => { setRestartOpen(false); setNotice(message); }} />}
      {automationOpen && <AutomationDialog enabled={overview.automation.enabled} onClose={() => setAutomationOpen(false)} onDone={(message) => { setAutomationOpen(false); setNotice(message); refresh(); }} />}
    </main>
  );
}

function Login({ onSuccess }: { onSuccess: () => void }) {
  const [error, setError] = useState("");
  const [needsBootstrap, setNeedsBootstrap] = useState(false);
  useEffect(() => { api<{ needs_bootstrap: boolean }>("/auth/bootstrap/status").then((x) => setNeedsBootstrap(x.needs_bootstrap)).catch(() => undefined); }, []);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    try {
      await api("/auth/login", { method: "POST", body: JSON.stringify({ username: form.get("username"), password: form.get("password"), totp_code: form.get("totp") }) });
      onSuccess();
    } catch (error) { setError(error instanceof Error && error.message === "rate_limited" ? "試行回数が多すぎます。しばらく待ってから再試行してください。" : "ユーザー名、パスワード、または認証コードを確認してください。"); }
  }
  if (needsBootstrap) return <Bootstrap onDone={() => setNeedsBootstrap(false)} />;
  return <main className="login-page"><section className="login-panel"><div className="brand login-brand"><div className="brand-orbit"><span /></div><div><strong>SHIDEN</strong><small>GUARDIAN</small></div></div><p className="eyebrow">SECURE OPERATOR ACCESS</p><h1>ノード運用へログイン</h1><p className="muted">管理操作はすべて記録され、再起動には再認証が必要です。</p><form onSubmit={submit}><label>ユーザー名<input name="username" autoComplete="username" required /></label><label>パスワード<input type="password" name="password" autoComplete="current-password" required /></label><label>認証コードまたはrecovery code<input name="totp" autoComplete="one-time-code" required /></label>{error && <p className="form-error">{error}</p>}<button className="primary-button" type="submit">ログイン</button></form></section></main>;
}

function Bootstrap({ onDone }: { onDone: () => void }) {
  const [error, setError] = useState("");
  const [setup, setSetup] = useState<{ token: string; username: string; secret: string; recovery: string[] } | null>(null);
  async function start(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form = new FormData(event.currentTarget); try { const result = await api<{ secret: string; recovery_codes: string[] }>("/auth/bootstrap/start", { method: "POST", body: JSON.stringify({ token: form.get("token"), username: form.get("username"), password: form.get("password") }) }); setSetup({ token: String(form.get("token")), username: String(form.get("username")), secret: result.secret, recovery: result.recovery_codes }); setError(""); } catch { setError("bootstrap tokenとパスワード要件を確認してください。"); } }
  async function confirm(event: FormEvent<HTMLFormElement>) { event.preventDefault(); if (!setup) return; const form = new FormData(event.currentTarget); try { await api("/auth/bootstrap/confirm", { method: "POST", body: JSON.stringify({ token: setup.token, username: setup.username, totp_code: form.get("totp") }) }); onDone(); } catch { setError("認証コードを確認してください。"); } }
  return <main className="login-page"><section className="login-panel setup-panel"><div className="brand login-brand"><div className="brand-orbit"><span /></div><div><strong>SHIDEN</strong><small>GUARDIAN</small></div></div><p className="eyebrow">FIRST OPERATOR SETUP</p><h1>初期管理者を設定</h1>{!setup ? <><p className="muted">デプロイ時のbootstrap tokenを使います。パスワードは14文字以上・3種類以上の文字種が必要です。</p><form onSubmit={start}><label>Bootstrap token<input name="token" type="password" required /></label><label>ユーザー名<input name="username" required /></label><label>管理者パスワード<input name="password" type="password" minLength={14} required /></label>{error && <p className="form-error">{error}</p>}<button className="primary-button">TOTPを準備</button></form></> : <><p className="muted">認証アプリへsecretを登録し、recovery codesを安全な場所へ保存してください。</p><code className="setup-secret">{setup.secret}</code><div className="recovery-grid">{setup.recovery.map((code) => <code key={code}>{code}</code>)}</div><form onSubmit={confirm}><label>認証アプリの6桁コード<input name="totp" inputMode="numeric" pattern="[0-9]{6}" required /></label>{error && <p className="form-error">{error}</p>}<button className="primary-button">管理者を有効化</button></form></>}</section></main>;
}

function OverviewTab({ data, demo, onRestart }: { data: Overview; demo: boolean; onRestart: () => void }) {
  const healthy = data.node.service_state === "active" && data.chain.status === "healthy";
  const demoBlocks = useMemo<LoadedSeries[]>(() => {
    const external = chartValues.map((_, index) => 9_842_610 + index * 4);
    const local = external.map((value, index) => value - (index % 7 === 0 ? 3 : index % 5 === 0 ? 1 : 0));
    return [
      { id: "guardian_local_finalized", label: "ローカル finalized", metric: { __name__: "guardian_local_finalized" }, points: makeDemoPoints(local, "30m") },
      { id: "guardian_external_height", label: "外部RPC", metric: { __name__: "guardian_external_height" }, points: makeDemoPoints(external, "30m") },
    ];
  }, []);
  const blocks = usePrometheusPanel("blocks", "30m", demo, demoBlocks);
  const blockSeries = useMemo<ChartSeries[]>(() => blocks.series.map((item) => {
    const external = item.id.includes("external");
    return { ...item, label: external ? "外部RPC" : "ローカル finalized", color: external ? "#5bb8ff" : "#a78bfa" };
  }).sort((left) => left.id.includes("local") ? -1 : 1), [blocks.series]);
  const blockValues = seriesValues(blockSeries);
  const blockMin = blockValues.length ? Math.min(...blockValues) : 0;
  const blockMax = blockValues.length ? Math.max(...blockValues) : 1;
  const blockPadding = Math.max(2, Math.ceil((blockMax - blockMin) * 0.08));

  return <div className="page-content">
    <section className="hero-status">
      <div className="hero-copy"><span className={healthy ? "status-pill healthy" : "status-pill critical"}><i />{healthy ? "ALL SYSTEMS NOMINAL" : "ATTENTION REQUIRED"}</span><h2>{healthy ? "Collatorは正常に稼働中" : "確認が必要な状態です"}</h2><p>ローカル状態と2系統の公開RPCを照合しています。現在、自動操作の条件は成立していません。</p></div>
      <div className="block-readout"><span>FINALIZED BLOCK</span><strong>{fmtNumber(data.chain.local_finalized)}</strong><small>外部差分 <b>{data.chain.lag}</b> blocks</small></div>
    </section>

    <div className="metric-grid">
      <MetricCard label="SERVICE" value={data.node.service_state.toUpperCase()} sub={`稼働 ${fmtUptime(data.node.uptime_seconds)}`} accent="green" />
      <MetricCard label="PEERS" value={String(data.chain.peers)} sub="警告しきい値 3" accent="violet" />
      <MetricCard label="CPU" value={`${data.host.cpu_percent.toFixed(0)}%`} sub={`温度 ${data.host.temperature_c.toFixed(0)}°C`} accent="blue" />
      <MetricCard label="MEMORY" value={`${data.host.memory_percent.toFixed(0)}%`} sub="10分平均は正常" accent="amber" />
    </div>

    <div className="dashboard-grid">
      <section className="panel chain-panel"><PanelTitle overline="CHAIN PROGRESS" title="ブロック進行" action="直近30分" /><TimeSeriesChart ariaLabel="ローカルと外部RPCのブロック高" series={blockSeries} loading={blocks.loading} error={blocks.error} yDomain={[Math.max(0, blockMin - blockPadding), blockMax + blockPadding]} formatValue={(value) => fmtNumber(Math.round(value))} /><div className="chain-stats"><div><span>LOCAL BEST</span><strong>{fmtNumber(data.chain.local_best)}</strong></div><div><span>EXTERNAL</span><strong>{fmtNumber(data.chain.external_height)}</strong></div><div><span>SYNC LAG</span><strong>{data.chain.lag} blocks</strong></div></div></section>
      <section className="panel health-panel"><PanelTitle overline="HOST HEALTH" title="ホスト余力" /><Gauge label="メモリ" value={data.host.memory_percent} warn={90} /><Gauge label="ディスク" value={data.host.disk_percent} warn={85} /><Gauge label="CPU" value={data.host.cpu_percent} warn={90} /><div className="health-note"><span className="tiny-dot" /> 自動復旧を妨げるリソース異常はありません</div></section>
      <section className="panel reward-overview-panel"><PanelTitle overline="COLLATOR REWARDS" title="報酬・ブロック生成" action={data.rewards.status.toUpperCase()} /><div className="reward-status-line"><span className={`reward-health ${data.rewards.status}`}><i />{data.rewards.active_session ? "ACTIVE SET" : "NOT ACTIVE"}</span><span>証拠 {data.rewards.quorum}/3</span></div><dl className="reward-overview-stats"><div><dt>最終報酬</dt><dd>{data.rewards.last_reward_at ? `${fmtDuration(data.rewards.seconds_since_reward)}前` : "収集中"}</dd></div><div><dt>24時間</dt><dd>{fmtPlanck(data.rewards.reward_24h_planck)}</dd><small>{fmtNumber(data.rewards.reward_24h_count)} blocks</small></div><div><dt>ウォレット残高</dt><dd>{fmtPlanck(data.rewards.wallet_free_planck)}</dd></div></dl><p className="panel-note">報酬異常は通知とAI診断のみを行い、自動再起動の根拠には使用しません。</p></section>
      <section className="panel incident-panel"><PanelTitle overline="LATEST SIGNAL" title="直近のインシデント" action={`${data.incidents.length}件`} />{data.incidents.length ? data.incidents.slice(0, 2).map((item) => <IncidentRow key={item.id} item={item} />) : <Empty title="インシデントはありません" text="異常を検知すると、証拠とAI診断がここに表示されます。" />}</section>
      <section className="panel automation-panel"><PanelTitle overline="REMEDIATION" title="復旧ガード" /><div className="guard-state"><span className="guard-icon">G</span><div><strong>{data.automation.host_locked ? "緊急停止ロック中" : data.automation.enabled ? "自動復旧 有効" : "観測モード"}</strong><p>{data.automation.host_locked ? "ホスト側で全restartを拒否しています。" : data.automation.enabled ? "安全条件が成立した場合のみ再起動します。" : "AIは診断しますが、自動操作は行いません。"}</p></div></div><ul className="guard-list"><li><span>AI confidence</span><b>0.90以上</b></li><li><span>Cooldown</span><b>60分</b></li><li><span>上限</span><b>2回 / 24時間</b></li></ul><button className="outline-button danger" onClick={onRestart}>手動再起動を開く</button></section>
    </div>
  </div>;
}

function MetricsTab({ data, demo }: { data: Overview; demo: boolean }) {
  const [range, setRange] = useState("24h");
  const demoSeries = useMemo(() => ({
    cpu: [{ id: "guardian_host_cpu_percent", label: "CPU", metric: {}, points: makeDemoPoints(chartValues, range) }],
    memory: [{ id: "guardian_host_memory_percent", label: "メモリ", metric: {}, points: makeDemoPoints(chartValues.map((value) => Math.min(94, value + 14)), range) }],
    peers: [{ id: "guardian_peers", label: "Peer数", metric: {}, points: makeDemoPoints(chartValues.map((value) => Math.round(value * 0.45 + 17)), range) }],
    lag: [{ id: "guardian_sync_lag", label: "同期差", metric: {}, points: makeDemoPoints([2, 1, 0, 3, 2, 4, 1, 0, 6, 3, 2, 1, 0, 4, 2, 1, 0, 3, 2, 1, 0, 0, 5, 3, 2, 1, 0, 2, 1, 0], range) }],
  }), [range]);
  const cpu = usePrometheusPanel("cpu", range, demo, demoSeries.cpu);
  const memory = usePrometheusPanel("memory", range, demo, demoSeries.memory);
  const peers = usePrometheusPanel("peers", range, demo, demoSeries.peers);
  const lag = usePrometheusPanel("lag", range, demo, demoSeries.lag);

  const definitions = [
    { id: "cpu", label: "CPU使用率", current: data.host.cpu_percent, state: cpu, color: "#5bb8ff", fill: true, curve: "linear" as const, thresholds: [{ value: 90, label: "高負荷の目安 90%", color: "#ff6b7a", band: "above" as const }], domain: [0, 100] as [number, number], format: (value: number) => `${value.toFixed(0)}%`, stats: (values: number[]) => [{ label: "現在", value: latestValue(cpu.series) ?? data.host.cpu_percent }, { label: "平均", value: average(values) }, { label: "最大", value: values.length ? Math.max(...values) : data.host.cpu_percent }] },
    { id: "memory", label: "メモリ使用率", current: data.host.memory_percent, state: memory, color: "#f2bd66", fill: true, curve: "linear" as const, thresholds: [{ value: 90, label: "重大 90%", color: "#ff6b7a", band: "above" as const }], domain: [0, 100] as [number, number], format: (value: number) => `${value.toFixed(0)}%`, stats: (values: number[]) => [{ label: "現在", value: latestValue(memory.series) ?? data.host.memory_percent }, { label: "平均", value: average(values) }, { label: "最大", value: values.length ? Math.max(...values) : data.host.memory_percent }] },
    { id: "peers", label: "Peer数", current: data.chain.peers, state: peers, color: "#50e3a4", fill: false, curve: "step" as const, thresholds: [{ value: 3, label: "警告 3未満", color: "#ff6b7a", band: "below" as const }], domain: [0, Math.max(6, Math.ceil(Math.max(0, ...seriesValues(peers.series)) * 1.15))] as [number, number], format: (value: number) => `${Math.round(value)}`, stats: (values: number[]) => [{ label: "現在", value: latestValue(peers.series) ?? data.chain.peers }, { label: "最小", value: values.length ? Math.min(...values) : data.chain.peers }, { label: "平均", value: average(values) }] },
    { id: "lag", label: "同期差", current: data.chain.lag, state: lag, color: "#a78bfa", fill: false, curve: "linear" as const, scale: "symlog" as const, thresholds: [{ value: 30, label: "警告 30", color: "#f2bd66" }, { value: 120, label: "重大 120", color: "#ff6b7a" }], domain: [0, Math.max(120, Math.ceil(Math.max(0, ...seriesValues(lag.series)) * 1.1))] as [number, number], format: (value: number) => `${Math.round(value)} blocks`, stats: (values: number[]) => [{ label: "現在", value: latestValue(lag.series) ?? data.chain.lag }, { label: "p95", value: percentile(values, 0.95) }, { label: "最大", value: values.length ? Math.max(...values) : data.chain.lag }] },
  ];

  return <div className="page-content"><div className="section-intro"><div><span className="eyebrow">15 SECOND SCRAPE</span><h2>ノードとホストの時系列</h2></div><select aria-label="表示期間" value={range} onChange={(event) => setRange(event.target.value)}><option value="1h">直近1時間</option><option value="24h">直近24時間</option><option value="7d">直近7日</option><option value="30d">直近30日</option></select></div><div className="metrics-layout">{definitions.map((panel) => {
    const values = seriesValues(panel.state.series);
    const chartSeries: ChartSeries[] = panel.state.series.map((item) => ({ ...item, label: panel.label, color: panel.color, curve: panel.curve, fill: panel.fill }));
    return <section className="panel metric-chart" key={panel.id}><PanelTitle overline="PROMETHEUS" title={panel.label} action={panel.format(latestValue(panel.state.series) ?? panel.current)} /><TimeSeriesChart ariaLabel={`${panel.label}の時系列`} series={chartSeries} thresholds={panel.thresholds} loading={panel.state.loading} error={panel.state.error} yDomain={panel.domain} scale={"scale" in panel ? panel.scale : "linear"} formatValue={panel.format} summaries={panel.stats(values).map((item) => ({ label: item.label, value: panel.format(item.value) }))} /></section>;
  })}</div></div>;
}

function RewardsTab({ overview, demo }: { overview: RewardOverview; demo: boolean }) {
  const [page, setPage] = useState<RewardPage>({ summary: overview, items: [], daily: [] });
  const [loading, setLoading] = useState(!demo);
  const [error, setError] = useState("");
  const demoItems = useMemo<RewardObservation[]>(() => Array.from({ length: 24 }, (_, index) => ({
    block_number: 9_842_704 - index * 12,
    block_hash: `0x${String(index).padStart(64, "a")}`,
    authored_at: new Date(Date.UTC(2026, 7, 5, 1, 30) - index * 72_000).toISOString(),
    expected_planck: "241000000000000000",
    credited_planck: "241000000000000000",
    verification: "confirmed",
    source_count: 3,
  })), []);
  const demoDaily = useMemo<RewardDaily[]>(() => Array.from({ length: 7 }, (_, index) => ({ day: new Date(Date.UTC(2026,7,index+1)).toISOString().slice(0,10), amount_planck: String(BigInt(270+index*4)*(BigInt(10)**BigInt(18))), count: 1120+index*15 })), []);

  useEffect(() => {
    if (demo) return;
    let stopped=false; let current:AbortController|null=null;
    const load=async()=>{ current?.abort(); current=new AbortController(); try { const result=await api<RewardPage>("/rewards?limit=100",{signal:current.signal}); if(!stopped){setPage(result);setLoading(false);setError("");} } catch(e){ if(e instanceof DOMException&&e.name==="AbortError")return; if(!stopped){setLoading(false);setError("報酬履歴を取得できませんでした。15秒後に再試行します。");} } };
    void load(); const timer=window.setInterval(load,15000); return()=>{stopped=true;current?.abort();window.clearInterval(timer);};
  },[demo]);

  const displayedPage = demo ? { summary: overview, items: demoItems, daily: demoDaily } : page;
  const summary = displayedPage.summary;
  const chronological = useMemo(()=>[...displayedPage.items].sort((a,b)=>new Date(a.authored_at).getTime()-new Date(b.authored_at).getTime()),[displayedPage.items]);
  const intervals = useMemo<ChartSeries[]>(()=>{
    const points=chronological.slice(1).map((item,index)=>({timestamp:new Date(item.authored_at).getTime()/1000,value:(new Date(item.authored_at).getTime()-new Date(chronological[index].authored_at).getTime())/1000}));
    return points.length?[{id:"reward_interval",label:"報酬間隔",metric:{},points,color:"#50e3a4"}]:[];
  },[chronological]);
  const cumulativeDemo = useMemo<LoadedSeries[]>(()=>[{id:"guardian_collator_reward_sdn_total",label:"累計報酬",metric:{__name__:"guardian_collator_reward_sdn_total"},points:makeDemoPoints(chartValues.map((_,i)=>810+i*2),"24h")}],[]);
  const prometheus=usePrometheusPanel("rewards","24h",demo,cumulativeDemo);
  const cumulative=useMemo<ChartSeries[]>(()=>prometheus.series.filter(item=>item.id.includes("reward_sdn_total")).map(item=>({...item,label:"累計報酬",color:"#a78bfa",fill:true})),[prometheus.series]);
  const cumulativeValues=seriesValues(cumulative); const cumulativeMin=cumulativeValues.length?Math.min(...cumulativeValues):0; const cumulativeMax=cumulativeValues.length?Math.max(...cumulativeValues):1;
  const intervalValues=seriesValues(intervals); const intervalMax=Math.max(1800,...intervalValues);

  return <div className="page-content reward-page">
    <div className="section-intro"><div><span className="eyebrow">ON-CHAIN REWARD PROOF</span><h2>ブロック生成報酬</h2><p className="section-copy">ローカルRPCと外部2系統で、作成block・報酬ポット・ウォレット入金を照合します。</p></div><a className="outline-button reward-link" href={`https://shiden.subscan.io/account/${summary.address}`} target="_blank" rel="noreferrer">Subscanで確認 ↗</a></div>
    <section className={`reward-hero ${summary.status}`}><div><span className={`reward-health ${summary.status}`}><i />{summary.status.toUpperCase()}</span><h3>{summary.active_session ? "コレーター報酬を正常に確認中" : "active setを確認してください"}</h3><p>{summary.last_reward_at ? `最終確認 ${fmtTime(summary.last_reward_at)}（${fmtDuration(summary.seconds_since_reward)}前）` : "監視開始後の最初の報酬を待っています。"}</p></div><div className="reward-address"><span>REWARD WALLET</span><code title={summary.address}>{summary.address}</code><small>runtime spec {summary.spec_version || "—"} · quorum {summary.quorum}/3</small></div></section>
    <div className="reward-card-grid">
      <MetricCard label="ACTIVE SET" value={summary.active_session ? `${summary.validator_count}中` : "対象外"} sub={summary.active_session ? "現セッションに所属" : "重大確認が必要"} accent={summary.active_session ? "green" : "amber"} />
      <MetricCard label="LAST REWARD" value={summary.last_reward_at ? fmtDuration(summary.seconds_since_reward) : "収集中"} sub={`block #${fmtNumber(summary.last_authored_block || 0)}`} accent="violet" />
      <MetricCard label="24 HOURS" value={fmtPlanck(summary.reward_24h_planck)} sub={`${fmtNumber(summary.reward_24h_count)} blocks`} accent="blue" />
      <MetricCard label="WALLET" value={fmtPlanck(summary.wallet_free_planck)} sub="free balance" accent="amber" />
    </div>
    {(summary.gap||!summary.schema_ok||summary.quorum<2)&&<div className="reward-warning" role="status">報酬未取得とは判定していません。監視証拠が不足しています。{summary.gap&&<small>{summary.gap}</small>}</div>}
    <div className="metrics-layout reward-charts">
      <section className="panel metric-chart"><PanelTitle overline="PROMETHEUS" title="累計報酬" action={fmtPlanck(summary.reward_total_planck)} /><TimeSeriesChart ariaLabel="累計報酬の時系列" series={cumulative} loading={prometheus.loading} error={prometheus.error} yDomain={[Math.max(0,cumulativeMin-(cumulativeMax-cumulativeMin)*.1),cumulativeMax+(cumulativeMax-cumulativeMin||1)*.1]} formatValue={(value)=>`${value.toFixed(3)} SDN`} /></section>
      <section className="panel metric-chart"><PanelTitle overline="AUTHORSHIP" title="報酬間隔" action={summary.last_reward_at?fmtDuration(summary.seconds_since_reward):"収集中"} /><TimeSeriesChart ariaLabel="ブロック生成報酬の間隔" series={intervals} loading={loading} error={error} thresholds={[{value:900,label:"警告 15分",color:"#f2bd66"},{value:1800,label:"重大 30分",color:"#ff6b7a"}]} yDomain={[0,intervalMax]} formatValue={(value)=>fmtDuration(Math.round(value))} /></section>
    </div>
    <section className="panel reward-daily"><PanelTitle overline="DAILY TOTAL" title="日別報酬" action="JST" /><DailyRewardBars items={displayedPage.daily||[]} /></section>
    <section className="panel reward-ledger"><PanelTitle overline="VERIFIED LEDGER" title="最近の報酬" action={`${displayedPage.items.length}件`} />{displayedPage.items.length?<div className="reward-table"><div className="reward-table-head"><span>BLOCK</span><span>JST</span><span>確認額</span><span>検証</span><span>証拠</span></div>{displayedPage.items.map(item=><div className="reward-table-row" key={item.block_number}><a href={`https://shiden.subscan.io/block/${item.block_number}`} target="_blank" rel="noreferrer">#{fmtNumber(item.block_number)}</a><time>{fmtTime(item.authored_at)}</time><span title={`${item.credited_planck} Planck`}>{fmtPlanck(item.credited_planck)}</span><b className={`verification ${item.verification}`}>{item.verification}</b><span>{item.source_count}/3</span></div>)}</div>:<Empty title="報酬履歴を収集中です" text="導入前の履歴は推測せず、監視開始後にfinalizedとなった報酬だけを記録します。" />}</section>
  </div>;
}

function DailyRewardBars({items}:{items:RewardDaily[]}) {
  if(!items.length)return <Empty title="日別履歴を収集中です" text="確認済み報酬が蓄積されると日別の合計を表示します。" />;
  const values=items.map(item=>Number(BigInt(item.amount_planck||"0"))/1e18); const max=Math.max(...values,1);
  return <div className="daily-bars" role="img" aria-label="日別報酬額"><div className="daily-bars-plot">{items.map((item,index)=><div className="daily-bar-item" key={item.day}><span className="daily-value">{values[index].toFixed(1)}</span><div className="daily-bar-track"><i style={{height:`${Math.max(2,values[index]/max*100)}%`}} /></div><time>{new Intl.DateTimeFormat("ja-JP",{month:"numeric",day:"numeric",timeZone:"Asia/Tokyo"}).format(new Date(`${item.day}T00:00:00+09:00`))}</time><small>{fmtNumber(item.count)} blocks</small></div>)}</div></div>;
}

function LogsTab({ lines, demo }: { lines: LogLine[]; demo: boolean }) {
  const [query, setQuery] = useState("");
  const [priority, setPriority] = useState("");
  const [filtered, setFiltered] = useState<LogLine[]>(lines);
  useEffect(() => {
    if (demo) return;
    const timer = window.setTimeout(() => {
      const params = new URLSearchParams({ limit: "500" });
      if (query) params.set("query", query);
      if (priority) params.set("priority", priority);
      api<{ lines: LogLine[] }>(`/logs?${params}`).then((result) => setFiltered(result.lines)).catch(() => setFiltered([]));
    }, 300);
    return () => window.clearTimeout(timer);
  }, [query, priority, demo]);
  const sample: LogLine[] = [
    { cursor: "1", timestamp: "2026-08-04T13:42:19Z", priority: "info", message: "Imported #9842719 (0x86af…c92d)" },
    { cursor: "2", timestamp: "2026-08-04T13:42:11Z", priority: "info", message: "Idle (47 peers), best: #9842719, finalized #9842716" },
    { cursor: "3", timestamp: "2026-08-04T13:42:03Z", priority: "notice", message: "Starting collation for relay parent 0x19e4…74bd" },
  ];
  const shown = demo ? sample : (filtered.length || query || priority ? filtered : lines);
  return <div className="page-content"><div className="section-intro"><div><span className="eyebrow">SYSTEMD JOURNAL</span><h2>astar.service ログ</h2></div><div className="log-actions"><input placeholder="ログを検索" aria-label="ログを検索" value={query} onChange={(event) => setQuery(event.target.value)} /><select aria-label="優先度" value={priority} onChange={(event) => setPriority(event.target.value)}><option value="">すべて</option><option value="warning">warning以上</option><option value="error">error以上</option></select></div></div><section className="log-viewer"><div className="log-head"><span>JST</span><span>LEVEL</span><span>MESSAGE</span></div>{shown.map((line) => <div className="log-line" key={line.cursor}><time>{fmtTime(line.timestamp).split(" ").pop()}</time><span className={`level ${line.priority}`}>{line.priority}</span><code>{line.message}</code></div>)}{!shown.length && <Empty title="ログを取得できません" text="ホストエージェントとの接続を確認してください。" />}</section></div>;
}

function IncidentsTab({ incidents, demo, onDiagnose }: { incidents: Incident[]; demo: boolean; onDiagnose: () => void }) {
  return <div className="page-content"><div className="section-intro"><div><span className="eyebrow">EVIDENCE-BASED TRIAGE</span><h2>検知・診断・復旧の履歴</h2></div><button className="outline-button" onClick={onDiagnose}>現在の状態をAI診断</button></div><section className="panel incident-table"><div className="table-head"><span>重要度</span><span>内容</span><span>状態</span><span>発生日時</span></div>{incidents.map((item) => <div className="table-row" key={item.id}><span><b className={`severity ${item.severity}`}>{item.severity}</b></span><span><strong>{item.title}</strong><small>{item.diagnosis || "診断待ち"}</small></span><span>{item.status}</span><time>{fmtTime(item.opened_at)}</time></div>)}{!incidents.length && <Empty title="履歴はありません" text="検知した異常とAI診断を365日保存します。" />}{demo && <p className="demo-foot">プレビュー用のサンプルインシデントを表示しています。</p>}</section></div>;
}

function MaintenanceTab({ data, onRestart }: { data: Overview; onRestart: () => void }) {
  return <div className="page-content"><div className="maintenance-hero"><div><span className="eyebrow">CONTROLLED ACTIONS ONLY</span><h2>安全境界内のメンテナンス</h2><p>`astar.service` の再起動だけを許可しています。停止、更新、DB修復、ホスト再起動は実行できません。</p></div><div className="lock-badge">LOCKED SCOPE</div></div><div className="maintenance-grid"><section className="panel"><PanelTitle overline="TARGET" title="astar.service" /><dl className="detail-list"><div><dt>状態</dt><dd className="good">{data.node.service_state}</dd></div><div><dt>再起動回数</dt><dd>{data.node.restart_count}</dd></div><div><dt>Cooldown</dt><dd>60分</dd></div><div><dt>過去24時間</dt><dd>0 / 2</dd></div></dl><button className="primary-button danger-fill" onClick={onRestart}>再起動を申請</button></section><section className="panel"><PanelTitle overline="HARD GUARDS" title="エージェント側の拒否条件" /><ul className="check-list"><li>緊急停止ロックが存在する</li><li>前回操作から60分未満</li><li>24時間で2回以上実行済み</li><li>直前の復旧確認が失敗</li><li>同じaction IDを処理済み</li></ul><p className="panel-note">Webアプリが誤動作しても、ホストエージェントが独立して拒否します。</p></section></div></div>;
}

function auditError(item: Audit) {
  const value = item.details?.error;
  return typeof value === "string" && value ? value.slice(0, 320) : "";
}

function SettingsTab({ data, audits, demo, setNotice, onAuditRefresh, onAutomation }: { data: Overview; audits: Audit[]; demo: boolean; setNotice: (x: string) => void; onAuditRefresh: () => Promise<void>; onAutomation: () => void }) {
  const sample: Audit[] = [{ id: "a1", action: "session.login", actor: "admin", created_at: new Date().toISOString(), result: "success" }];
  const [testing, setTesting] = useState<"" | "smtp" | "gemini">("");
  async function testIntegration(kind: "smtp" | "gemini") {
    if (demo) { setNotice("プレビューでは外部サービスへ接続しません。"); return; }
    setTesting(kind);
    try {
      await api(`/settings/test-${kind}`, { method: "POST", body: "{}" });
      setNotice(`${kind === "smtp" ? "SMTP" : "Gemini"}疎通テストをキューへ登録しました。監査履歴へ結果が反映されます。`);
      await onAuditRefresh();
    } catch {
      setNotice("疎通テストの登録に失敗しました。");
    } finally {
      setTesting("");
    }
  }
  return <div className="page-content"><div className="settings-grid"><section className="panel"><PanelTitle overline="AUTOMATION" title="自動復旧" /><div className="setting-row"><div><strong>観測モード</strong><p>14日間はAI診断のみを蓄積します。</p></div><div><span className="state-chip">{data.automation.host_locked ? "host locked" : data.automation.mode}</span><button className="small-button" onClick={onAutomation}>{data.automation.enabled ? "無効化" : "有効化"}</button></div></div><div className="setting-row"><div><strong>メール通知</strong><p>異常、診断、操作結果を送信します。</p></div><button className="small-button" disabled={testing === "smtp"} onClick={() => testIntegration("smtp")}>{testing === "smtp" ? "登録中…" : "テスト"}</button></div><div className="setting-row"><div><strong>Gemini API</strong><p>store=false・構造化出力</p></div><button className="small-button" disabled={testing === "gemini"} onClick={() => testIntegration("gemini")}>{testing === "gemini" ? "登録中…" : "テスト"}</button></div></section><section className="panel audit-panel"><PanelTitle overline="AUDIT TRAIL" title="監査履歴" />{(demo ? sample : audits).map((item) => { const error = auditError(item); return <div className="audit-row" key={item.id}><span className="audit-mark">A</span><div><strong>{item.action}</strong><small>{item.actor} · {fmtTime(item.created_at)}</small>{error && <small className="audit-error">{error}</small>}</div><b className={`audit-result ${item.result}`}>{item.result}</b></div>; })}</section></div></div>;
}

function AutomationDialog({ enabled, onClose, onDone }: { enabled: boolean; onClose: () => void; onDone: (x: string) => void }) {
  const [error, setError] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form = new FormData(event.currentTarget); try { await api("/settings", { method: "PUT", body: JSON.stringify({ automation_enabled: !enabled, password: form.get("password"), totp_code: form.get("totp") }) }); onDone(`自動復旧を${enabled ? "無効" : "有効"}にしました。`); } catch { setError(enabled ? "設定を変更できませんでした。" : "14日間の観測期間と再認証情報を確認してください。"); } }
  return <div className="modal-backdrop" role="presentation"><section className="modal" role="dialog" aria-modal="true" aria-labelledby="automation-title"><button className="modal-close" onClick={onClose}>×</button><span className="eyebrow">STEP-UP AUTHENTICATION</span><h2 id="automation-title">自動復旧を{enabled ? "無効化" : "有効化"}</h2><p>{enabled ? "自動再起動を停止します。手動操作は引き続き安全条件の対象です。" : "14日間の観測完了後、安全条件がすべて成立した場合だけ自動再起動します。"}</p><form onSubmit={submit}><label>パスワード<input name="password" type="password" required /></label><label>6桁の認証コード<input name="totp" inputMode="numeric" pattern="[0-9]{6}" required /></label>{error && <p className="form-error">{error}</p>}<div className="modal-actions"><button type="button" className="outline-button" onClick={onClose}>キャンセル</button><button type="submit" className="primary-button">設定を保存</button></div></form></section></div>;
}

function RestartDialog({ demo, onClose, onDone }: { demo: boolean; onClose: () => void; onDone: (x: string) => void }) {
  const [error, setError] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    if (form.get("confirm") !== "tk_sdn_collator") { setError("確認欄へ tk_sdn_collator と入力してください。"); return; }
    if (demo) { onDone("プレビューでは再起動を実行しません。操作フローだけを確認しました。"); return; }
    try { await api("/actions/restart", { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ password: form.get("password"), totp_code: form.get("totp"), reason: form.get("reason"), confirm: form.get("confirm") }) }); onDone("再起動申請を受け付けました。結果は画面とメールで通知します。"); } catch (e) { setError(e instanceof Error ? e.message : "申請に失敗しました。"); }
  }
  return <div className="modal-backdrop" role="presentation"><section className="modal" role="dialog" aria-modal="true" aria-labelledby="restart-title"><button className="modal-close" onClick={onClose}>×</button><span className="eyebrow">STEP-UP AUTHENTICATION</span><h2 id="restart-title">astar.service を再起動</h2><p>操作はキューへ登録され、ホスト側の安全条件を通過した場合に1回だけ実行されます。</p><form onSubmit={submit}><label>理由<textarea name="reason" minLength={10} required placeholder="再起動が必要な根拠を入力" /></label><label>パスワード<input name="password" type="password" required /></label><label>6桁の認証コード<input name="totp" inputMode="numeric" pattern="[0-9]{6}" required /></label><label>確認のためノード名を入力<input name="confirm" placeholder="tk_sdn_collator" required /></label>{error && <p className="form-error">{error}</p>}<div className="modal-actions"><button type="button" className="outline-button" onClick={onClose}>キャンセル</button><button type="submit" className="primary-button danger-fill">申請する</button></div></form></section></div>;
}

function MetricCard({ label, value, sub, accent }: { label: string; value: string; sub: string; accent: string }) { return <article className={`metric-card ${accent}`}><span>{label}</span><strong>{value}</strong><small>{sub}</small></article>; }
function PanelTitle({ overline, title, action }: { overline: string; title: string; action?: string }) { return <header className="panel-title"><div><span>{overline}</span><h3>{title}</h3></div>{action && <b>{action}</b>}</header>; }

function chartPath(points: TimePoint[], minTime: number, maxTime: number, valueToY: (value: number) => number, curve: "linear" | "step" = "linear") {
  const x = (timestamp: number) => maxTime === minTime ? 500 : ((timestamp - minTime) / (maxTime - minTime)) * 1000;
  return points.map((point, index) => {
    const px = x(point.timestamp).toFixed(2);
    const py = valueToY(point.value).toFixed(2);
    if (index === 0) return `M ${px} ${py}`;
    return curve === "step" ? `H ${px} V ${py}` : `L ${px} ${py}`;
  }).join(" ");
}

function nearestPoint(points: TimePoint[], timestamp: number) {
  return points.reduce<TimePoint | undefined>((best, point) => !best || Math.abs(point.timestamp - timestamp) < Math.abs(best.timestamp - timestamp) ? point : best, undefined);
}

function formatChartTime(timestamp: number, includeDate = false) {
  return new Intl.DateTimeFormat("ja-JP", { timeZone: "Asia/Tokyo", ...(includeDate ? { month: "2-digit", day: "2-digit" } : {}), hour: "2-digit", minute: "2-digit" }).format(new Date(timestamp * 1000));
}

function TimeSeriesChart({ ariaLabel, series, thresholds = [], loading, error, yDomain, scale = "linear", formatValue, summaries = [] }: { ariaLabel: string; series: ChartSeries[]; thresholds?: ChartThreshold[]; loading: boolean; error: string; yDomain: [number, number]; scale?: "linear" | "symlog"; formatValue: (value: number) => string; summaries?: { label: string; value: string }[] }) {
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const allPoints = series.flatMap((item) => item.points);
  const primary = series.reduce<ChartSeries | undefined>((best, item) => !best || item.points.length > best.points.length ? item : best, undefined);
  const minTime = allPoints.length ? Math.min(...allPoints.map((point) => point.timestamp)) : 0;
  const maxTime = allPoints.length ? Math.max(...allPoints.map((point) => point.timestamp)) : 0;
  const minValue = yDomain[0];
  const maxValue = yDomain[1] > yDomain[0] ? yDomain[1] : yDomain[0] + 1;
  const transformValue = (value: number) => scale === "symlog" ? Math.log1p(Math.max(0, value)) : value;
  const inverseValue = (value: number) => scale === "symlog" ? Math.expm1(value) : value;
  const transformedMin = transformValue(minValue);
  const transformedMax = transformValue(maxValue);
  const valueToY = (value: number) => 300 - ((transformValue(value) - transformedMin) / Math.max(1e-9, transformedMax - transformedMin)) * 300;
  const activePoint = activeIndex === null ? undefined : primary?.points[Math.min(activeIndex, Math.max(0, primary.points.length - 1))];
  const activeRatio = activePoint && maxTime !== minTime ? (activePoint.timestamp - minTime) / (maxTime - minTime) : 0.5;
  const includeDate = maxTime - minTime >= 86_400;
  const axisValues = [maxValue, inverseValue((transformedMax + transformedMin) / 2), minValue];

  if (loading && !allPoints.length) return <div className="chart-state" role="status"><span className="chart-spinner" />履歴を読み込んでいます…</div>;
  if (!allPoints.length) return <div className="chart-state chart-state-error" role="status"><strong>時系列データがありません</strong><span>{error || "次のPrometheus収集後に自動で表示します。"}</span></div>;

  return <div className="time-series">
    <div className="chart-meta">
      <div className="chart-legend">{series.map((item) => <span key={item.id}><i style={{ background: item.color }} />{item.label}</span>)}</div>
      {thresholds.length > 0 && <div className="chart-thresholds">{thresholds.map((item) => <span key={`${item.label}-${item.value}`}><i style={{ borderColor: item.color }} />{item.label}</span>)}</div>}
    </div>
    <div className="chart-layout">
      <div className="chart-y-axis" aria-hidden="true">{axisValues.map((value, index) => <span key={`${value}-${index}`}>{formatValue(value)}</span>)}</div>
      <div className="chart-canvas" role="img" aria-label={ariaLabel} tabIndex={0}
        onPointerMove={(event) => {
          if (!primary?.points.length) return;
          const rect = event.currentTarget.getBoundingClientRect();
          const ratio = Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width));
          setActiveIndex(Math.round(ratio * (primary.points.length - 1)));
        }}
        onPointerLeave={() => setActiveIndex(null)}
        onFocus={() => setActiveIndex((current) => current ?? Math.max(0, (primary?.points.length ?? 1) - 1))}
        onBlur={() => setActiveIndex(null)}
        onKeyDown={(event) => {
          if (!primary?.points.length || (event.key !== "ArrowLeft" && event.key !== "ArrowRight")) return;
          event.preventDefault();
          const direction = event.key === "ArrowRight" ? 1 : -1;
          setActiveIndex((current) => Math.min(primary.points.length - 1, Math.max(0, (current ?? primary.points.length - 1) + direction)));
        }}>
        <svg className="time-series-svg" viewBox="0 0 1000 300" preserveAspectRatio="none" aria-hidden="true">
          {[0, 75, 150, 225, 300].map((value) => <line key={value} x1="0" y1={value} x2="1000" y2={value} className="chart-grid-line" />)}
          {thresholds.map((threshold) => {
            const y = valueToY(threshold.value);
            if (y < 0 || y > 300) return null;
            return <g key={`${threshold.label}-${threshold.value}`}>{threshold.band && <rect x="0" y={threshold.band === "above" ? 0 : y} width="1000" height={threshold.band === "above" ? y : 300 - y} fill={threshold.color} opacity="0.06" />}<line x1="0" y1={y} x2="1000" y2={y} stroke={threshold.color} strokeWidth="2" strokeDasharray="10 9" opacity="0.8" vectorEffect="non-scaling-stroke" /></g>;
          })}
          {series.map((item) => {
            const path = chartPath(item.points, minTime, maxTime, valueToY, item.curve);
            const first = item.points[0];
            const last = item.points.at(-1);
            const area = item.fill && first && last ? `${path} L ${maxTime === minTime ? 500 : 1000} 300 L ${maxTime === minTime ? 500 : 0} 300 Z` : "";
            return <g key={item.id}>{area && <path d={area} fill={item.color} opacity="0.12" />}<path d={path} fill="none" stroke={item.color} strokeWidth="3" strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />{item.points.length === 1 && <circle cx="500" cy={valueToY(item.points[0].value)} r="6" fill={item.color} vectorEffect="non-scaling-stroke" />}</g>;
          })}
        </svg>
        {activePoint && <><span className="chart-crosshair" style={{ left: `${activeRatio * 100}%` }} /><div className={activeRatio > 0.68 ? "chart-tooltip align-right" : "chart-tooltip"} style={{ left: `${activeRatio * 100}%` }}><time>{formatChartTime(activePoint.timestamp, true)}</time>{series.map((item) => { const point = nearestPoint(item.points, activePoint.timestamp); return point ? <div key={item.id}><span><i style={{ background: item.color }} />{item.label}</span><strong>{formatValue(point.value)}</strong></div> : null; })}</div></>}
      </div>
    </div>
    <div className="chart-x-axis" aria-hidden="true"><span>{formatChartTime(minTime, includeDate)}</span><span>{formatChartTime((minTime + maxTime) / 2, includeDate)}</span><span>{formatChartTime(maxTime, includeDate)}</span></div>
    {primary?.points.length === 1 && <p className="chart-note">履歴を蓄積中です。次の収集後に線で表示します。</p>}
    {error && <p className="chart-note chart-note-error">{error}</p>}
    {summaries.length > 0 && <dl className="chart-summary">{summaries.map((item) => <div key={item.label}><dt>{item.label}</dt><dd>{item.value}</dd></div>)}</dl>}
  </div>;
}

function Gauge({ label, value, warn }: { label: string; value: number; warn: number }) { return <div className="gauge"><div><span>{label}</span><b>{value.toFixed(0)}%</b></div><div className="gauge-track"><i className={value >= warn ? "warn" : ""} style={{ width: `${value}%` }} /></div></div>; }
function IncidentRow({ item }: { item: Incident }) { return <div className="incident-row"><b className={`severity ${item.severity}`}>{item.severity}</b><div><strong>{item.title}</strong><small>{item.diagnosis}</small></div><time>{fmtTime(item.opened_at)}</time></div>; }
function Empty({ title, text }: { title: string; text: string }) { return <div className="empty"><span>—</span><strong>{title}</strong><p>{text}</p></div>; }
