"use client";

import Image from "next/image";
import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { I18nProvider, LanguageToggle, LocalizedMessage, MessageKey, MessageValues, useI18n } from "./i18n";

type Tab = "overview" | "metrics" | "rewards" | "logs" | "incidents" | "maintenance" | "settings";
type RewardRecovery = { required: boolean; reason: string; expected_cursor: number; candidate_resume_from_block: number };
type RewardHistoryGap = { from_block: number; through_block: number; resume_from_block: number; acknowledged_at: string; acknowledged_by: string; history_recovered: boolean };
type RewardOverview = {
  address: string; status: string; monitoring_started_at?: string; active_session: boolean; validator_count: number;
  finalized_block: number; last_scanned_block: number; last_authored_block: number; last_reward_at?: string;
  seconds_since_reward: number; blocks_since_authored: number; kick_blocks_remaining: number; wallet_free_planck: string;
  last_reward_planck: string; reward_24h_planck: string; reward_24h_count: number; reward_total_planck: string;
  reward_total_count: number; spec_version: number; schema_ok: boolean; quorum: number; inactive_confirmations?: number; sources: Record<string, string>; gap?: string;
  recovery?: RewardRecovery; last_history_gap?: RewardHistoryGap;
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
type PublicNodeStatus = {
  network: "shiden";
  overall: "operational" | "degraded" | "unavailable";
  node: "online" | "offline" | "unknown";
  sync: "synced" | "catching_up" | "unknown";
  finalized_block: number | null;
  observed_at: string | null;
};
type Incident = { id: string; fingerprint?: string; severity: string; title: string; status: string; opened_at: string; diagnosis?: string; evidence?: Record<string, unknown> };
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
type PanelState = { series: LoadedSeries[]; loading: boolean; error?: MessageKey };
type ChartSeries = LoadedSeries & { color: string; curve?: "linear" | "step"; fill?: boolean };
type ChartThreshold = { value: number; label: string; color: string; band?: "above" | "below" };

const tabs: { id: Tab; labelKey: MessageKey; mark: string }[] = [
  { id: "overview", labelKey: "tabs.overview", mark: "01" },
  { id: "metrics", labelKey: "tabs.metrics", mark: "02" },
  { id: "rewards", labelKey: "tabs.rewards", mark: "03" },
  { id: "logs", labelKey: "tabs.logs", mark: "04" },
  { id: "incidents", labelKey: "tabs.incidents", mark: "05" },
  { id: "maintenance", labelKey: "tabs.maintenance", mark: "06" },
  { id: "settings", labelKey: "tabs.settings", mark: "07" },
];

const demoOverview: Overview = {
  node: { name: "tk_sdn_collator", service_state: "active", version: "v5.48.1", uptime_seconds: 2_486_420, restart_count: 0 },
  chain: { local_finalized: 9_842_716, local_best: 9_842_719, external_height: 9_842_719, lag: 0, peers: 47, status: "healthy" },
  host: { cpu_percent: 32, memory_percent: 61, disk_percent: 54, temperature_c: 47 },
  automation: { mode: "observe_only", enabled: false, host_locked: false, eligible_at: "2026-08-18T12:00:00Z" },
  rewards: { address: "WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN", status: "healthy", monitoring_started_at: "2026-08-05T00:00:00Z", active_session: true, validator_count: 12, finalized_block: 9_842_716, last_scanned_block: 9_842_716, last_authored_block: 9_842_704, last_reward_at: new Date(Date.now()-72_000).toISOString(), seconds_since_reward: 72, blocks_since_authored: 12, kick_blocks_remaining: 1188, wallet_free_planck: "6278626775264166000000", last_reward_planck: "241000000000000000", reward_24h_planck: "289200000000000000000", reward_24h_count: 1200, reward_total_planck: "867600000000000000000", reward_total_count: 3600, spec_version: 2300, schema_ok: true, quorum: 3, sources: { local: "ok", external_1: "ok", external_2: "ok" } },
  incidents: [
    { id: "demo-1", fingerprint: "peers-low", severity: "warning", title: "Relay peer数が一時的に低下", status: "resolved", opened_at: "2026-08-03T03:41:00Z", diagnosis: "90秒以内に自然回復しました。操作は行っていません。", evidence: { peers: 2 } },
  ],
  updated_at: new Date().toISOString(),
};

const chartValues = [34, 38, 36, 44, 41, 52, 48, 45, 57, 54, 61, 58, 64, 59, 62, 67, 61, 57, 53, 49, 46, 51, 45, 43, 47, 39, 36, 42, 35, 32];
const rangeSeconds: Record<string, number> = { "30m": 1800, "1h": 3600, "6h": 21600, "24h": 86400, "7d": 604800, "30d": 2592000 };
const demoModeEnabled = process.env.NEXT_PUBLIC_DEMO_MODE === "true";

type MessageState = LocalizedMessage | string | null;

function message(key: MessageKey, values?: MessageValues): LocalizedMessage {
  return { key, values };
}

function localized(messageState: MessageState, t: (key: MessageKey, values?: MessageValues) => string) {
  if (!messageState) return "";
  return typeof messageState === "string" ? messageState : t(messageState.key, messageState.values);
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

function isPublicNodeStatus(value: unknown): value is PublicNodeStatus {
  if (!value || typeof value !== "object") return false;
  const status = value as Partial<PublicNodeStatus>;
  const finalized = status.finalized_block;
  return status.network === "shiden"
    && ["operational", "degraded", "unavailable"].includes(String(status.overall))
    && ["online", "offline", "unknown"].includes(String(status.node))
    && ["synced", "catching_up", "unknown"].includes(String(status.sync))
    && (finalized === null || (typeof finalized === "number" && Number.isSafeInteger(finalized) && finalized >= 0))
    && (status.observed_at === null || (typeof status.observed_at === "string" && Number.isFinite(Date.parse(status.observed_at))));
}

async function publicStatusApi(signal: AbortSignal): Promise<PublicNodeStatus> {
  const response = await fetch("/api/v1/public/status", {
    method: "GET",
    credentials: "omit",
    headers: { Accept: "application/json" },
    signal,
  });
  if (!response.ok) throw new Error("public_status_unavailable");
  const value: unknown = await response.json();
  if (!isPublicNodeStatus(value)) throw new Error("public_status_invalid");
  return value;
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
  const [state, setState] = useState<PanelState>({ series: [], loading: true });
  useEffect(() => {
    if (demo) return;

    let stopped = false;
    let current: AbortController | null = null;
    async function load() {
      current?.abort();
      current = new AbortController();
      try {
        const response = await api<PrometheusResponse>(`/metrics/${panel}?range=${range}`, { signal: current.signal });
        if (!stopped) setState({ series: parsePrometheus(response), loading: false });
      } catch (error) {
        if (error instanceof DOMException && error.name === "AbortError") return;
        if (!stopped) setState((previous) => ({ ...previous, loading: false, error: "chart.historyError" }));
      }
    }

    void load();
    const timer = window.setInterval(load, 15_000);
    return () => {
      stopped = true;
      current?.abort();
      window.clearInterval(timer);
    };
  }, [panel, range, demo]);
  return demo ? { series: demoSeries, loading: false } : state;
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

export default function Page() {
  return <I18nProvider><Dashboard /></I18nProvider>;
}

function Dashboard() {
  const { t, formatDateTime } = useI18n();
  const [tab, setTab] = useState<Tab>("overview");
  const [overview, setOverview] = useState<Overview>(demoOverview);
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [demo, setDemo] = useState(false);
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [audits, setAudits] = useState<Audit[]>([]);
  const [menuOpen, setMenuOpen] = useState(false);
  const [restartOpen, setRestartOpen] = useState(false);
  const [automationOpen, setAutomationOpen] = useState(false);
  const [rewardRecovery, setRewardRecovery] = useState<RewardRecovery | null>(null);
  const [notice, setNotice] = useState<MessageState>(null);

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

  const activeTab = tabs.find((item) => item.id === tab);
  const activeTabLabel = activeTab ? t(activeTab.labelKey) : "";

  if (authenticated === null) return <main className="login-shell"><section className="login-card"><div className="brand-orbit large"><span /></div><p>{t("loading.connecting")}</p></section></main>;
  if (authenticated === false) return <Login onSuccess={refresh} />;

  return (
    <main className="app-shell">
      <aside className={menuOpen ? "sidebar open" : "sidebar"}>
        <div className="brand">
          <div className="brand-orbit"><span /></div>
          <div><strong>SHIDEN</strong><small>GUARDIAN</small></div>
        </div>
        <div className="network-badge"><span className="pulse-dot" />KUSAMA · SHIDEN</div>
        <nav aria-label={t("nav.main")}>
          {tabs.map((item) => (
            <button key={item.id} className={tab === item.id ? "nav-item active" : "nav-item"} onClick={() => { setTab(item.id); setMenuOpen(false); }}>
              <span className="nav-mark">{item.mark}</span><span>{t(item.labelKey)}</span>
            </button>
          ))}
        </nav>
        <div className="sidebar-foot">
          <p>NODE</p><strong>{overview.node.name}</strong>
          <a href="https://telemetry.polkadot.io/#list/0xf1cf9022c7ebb34b162d5b5e34e705a5a740b2d0ecc1009fb89023e62a488108" target="_blank" rel="noreferrer">{t("nav.openTelemetry")}</a>
        </div>
      </aside>

      <section className="workspace">
        <header className="topbar">
          <button className="menu-button" aria-label={t("topbar.menu")} onClick={() => setMenuOpen((v) => !v)}>☰</button>
          <div>
            <span className="eyebrow">OPERATIONS / {activeTabLabel}</span>
            <h1>{activeTabLabel}</h1>
          </div>
          <div className="topbar-actions">
            {demo && <span className="demo-label">{t("topbar.preview")}</span>}
            <span className="last-update">{t("topbar.updated", { time: formatDateTime(overview.updated_at) })}</span>
            <LanguageToggle />
            <button className="refresh-button" onClick={refresh} aria-label={t("topbar.refresh")}>↻</button>
            <button className="small-button" onClick={async () => { try { await api("/auth/logout", { method: "POST", body: "{}" }); } finally { sessionStorage.removeItem("sg_csrf"); setAuthenticated(false); } }}>{t("topbar.logout")}</button>
          </div>
        </header>

        {notice && <div className="notice" role="status">{localized(notice, t)}<button aria-label={t("common.close")} onClick={() => setNotice(null)}>×</button></div>}
        {tab === "overview" && <OverviewTab data={overview} demo={demo} onRestart={() => setRestartOpen(true)} />}
        {tab === "metrics" && <MetricsTab data={overview} demo={demo} />}
        {tab === "rewards" && <RewardsTab overview={overview.rewards} demo={demo} onRecover={setRewardRecovery} />}
        {tab === "logs" && <LogsTab lines={logs} demo={demo} />}
        {tab === "incidents" && <IncidentsTab incidents={overview.incidents} demo={demo} onDiagnose={async () => { if (!demo) await api("/diagnoses", { method: "POST", body: "{}" }); setNotice(message("notice.diagnosisQueued")); }} />}
        {tab === "maintenance" && <MaintenanceTab data={overview} onRestart={() => setRestartOpen(true)} />}
        {tab === "settings" && <SettingsTab data={overview} audits={audits} demo={demo} setNotice={setNotice} onAuditRefresh={refreshAudits} onAutomation={() => setAutomationOpen(true)} />}
      </section>
      {restartOpen && <RestartDialog demo={demo} onClose={() => setRestartOpen(false)} onDone={(nextMessage) => { setRestartOpen(false); setNotice(nextMessage); }} />}
      {automationOpen && <AutomationDialog enabled={overview.automation.enabled} onClose={() => setAutomationOpen(false)} onDone={(nextMessage) => { setAutomationOpen(false); setNotice(nextMessage); refresh(); }} />}
      {rewardRecovery && <RewardRecoveryDialog recovery={rewardRecovery} onClose={() => setRewardRecovery(null)} onDone={(nextMessage) => { setRewardRecovery(null); setNotice(nextMessage); void refresh(); }} />}
    </main>
  );
}

function Login({ onSuccess }: { onSuccess: () => void }) {
  const { t } = useI18n();
  const [error, setError] = useState<MessageState>(null);
  const [needsBootstrap, setNeedsBootstrap] = useState<boolean | null>(null);
  useEffect(() => { api<{ needs_bootstrap: boolean }>("/auth/bootstrap/status").then((x) => setNeedsBootstrap(x.needs_bootstrap)).catch(() => setNeedsBootstrap(false)); }, []);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    try {
      await api("/auth/login", { method: "POST", body: JSON.stringify({ username: form.get("username"), password: form.get("password"), totp_code: form.get("totp") }) });
      onSuccess();
    } catch (submitError) { setError(message(submitError instanceof Error && submitError.message === "rate_limited" ? "auth.error.rateLimited" : "auth.error.credentials")); }
  }
  if (needsBootstrap === null) return <main className="login-shell"><section className="login-card"><div className="brand-orbit large"><span /></div><p>{t("loading.connecting")}</p></section></main>;
  if (needsBootstrap) return <Bootstrap onDone={() => setNeedsBootstrap(false)} />;
  return <main className="login-page"><LanguageToggle className="login-language-toggle" /><div className="login-layout"><section className="login-panel" aria-labelledby="login-title"><div className="brand login-brand"><div className="brand-orbit"><span /></div><div><strong>SHIDEN</strong><small>GUARDIAN</small></div></div><p className="eyebrow">{t("auth.eyebrow")}</p><h1 id="login-title">{t("auth.title")}</h1><form onSubmit={submit}><label>{t("auth.username")}<input name="username" autoComplete="username" required /></label><label>{t("auth.password")}<input type="password" name="password" autoComplete="current-password" required /></label><label>{t("auth.code")}<input name="totp" autoComplete="one-time-code" required /></label>{error && <p className="form-error">{localized(error, t)}</p>}<button className="primary-button" type="submit">{t("auth.submit")}</button></form></section><PublicStatusCard /></div></main>;
}

function PublicStatusCard() {
  const { t, formatDateTime, formatNumber } = useI18n();
  const [status, setStatus] = useState<PublicNodeStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let stopped = false;
    let current: AbortController | null = null;
    async function load() {
      current?.abort();
      current = new AbortController();
      try {
        const next = await publicStatusApi(current.signal);
        if (!stopped) {
          setStatus(next);
          setFailed(false);
          setLoading(false);
        }
      } catch (loadError) {
        if (loadError instanceof DOMException && loadError.name === "AbortError") return;
        if (!stopped) {
          setStatus(null);
          setFailed(true);
          setLoading(false);
        }
      }
    }
    void load();
    const timer = window.setInterval(load, 30_000);
    return () => {
      stopped = true;
      current?.abort();
      window.clearInterval(timer);
    };
  }, []);

  const overall = status?.overall ?? "unavailable";
  const node = status?.node ?? "unknown";
  const sync = status?.sync ?? "unknown";
  const overallKey = `publicStatus.overall.${overall}` as MessageKey;
  const nodeKey = `publicStatus.node.${node}` as MessageKey;
  const syncKey = `publicStatus.sync.${sync}` as MessageKey;
  const summary = loading ? t("publicStatus.loading") : failed ? t("publicStatus.unavailable") : t(`publicStatus.description.${overall}` as MessageKey);

  return <section className={`public-status-card ${overall}`} aria-labelledby="public-status-title" aria-busy={loading}>
    <div className="public-status-network"><span className="pulse-dot" aria-hidden="true" />KUSAMA · SHIDEN</div>
    <p className="eyebrow">{t("publicStatus.eyebrow")}</p>
    <h2 id="public-status-title">{t("publicStatus.title")}</h2>
    <div className="public-status-summary" aria-live="polite">
      <span className={`public-status-pill ${overall}`}><i aria-hidden="true" />{loading ? t("publicStatus.checking") : t(overallKey)}</span>
      <p>{summary}</p>
    </div>
    <dl className="public-status-grid">
      <div><dt>{t("publicStatus.node")}</dt><dd><span className={`public-state-dot ${node}`} aria-hidden="true" />{t(nodeKey)}</dd></div>
      <div><dt>{t("publicStatus.sync")}</dt><dd><span className={`public-state-dot ${sync}`} aria-hidden="true" />{t(syncKey)}</dd></div>
      <div className="public-status-block"><dt>{t("publicStatus.finalizedBlock")}</dt><dd>{status?.finalized_block == null ? "—" : formatNumber(status.finalized_block)}</dd></div>
    </dl>
    <p className="public-status-observed">{t("publicStatus.observedAt")}: <time dateTime={status?.observed_at ?? undefined}>{status?.observed_at ? formatDateTime(status.observed_at) : "—"}</time></p>
  </section>;
}

function Bootstrap({ onDone }: { onDone: () => void }) {
  const { t } = useI18n();
  const [error, setError] = useState<MessageState>(null);
  const [setup, setSetup] = useState<{ token: string; username: string; secret: string; recovery: string[] } | null>(null);
  async function start(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form = new FormData(event.currentTarget); try { const result = await api<{ secret: string; recovery_codes: string[] }>("/auth/bootstrap/start", { method: "POST", body: JSON.stringify({ token: form.get("token"), username: form.get("username"), password: form.get("password") }) }); setSetup({ token: String(form.get("token")), username: String(form.get("username")), secret: result.secret, recovery: result.recovery_codes }); setError(null); } catch { setError(message("bootstrap.error.start")); } }
  async function confirm(event: FormEvent<HTMLFormElement>) { event.preventDefault(); if (!setup) return; const form = new FormData(event.currentTarget); try { await api("/auth/bootstrap/confirm", { method: "POST", body: JSON.stringify({ token: setup.token, username: setup.username, totp_code: form.get("totp") }) }); onDone(); } catch { setError(message("bootstrap.error.code")); } }
  return <main className="login-page"><LanguageToggle className="login-language-toggle" /><section className="login-panel setup-panel"><div className="brand login-brand"><div className="brand-orbit"><span /></div><div><strong>SHIDEN</strong><small>GUARDIAN</small></div></div><p className="eyebrow">{t("bootstrap.eyebrow")}</p><h1>{t("bootstrap.title")}</h1>{!setup ? <><p className="muted">{t("bootstrap.description")}</p><form onSubmit={start}><label>{t("bootstrap.token")}<input name="token" type="password" required /></label><label>{t("bootstrap.username")}<input name="username" required /></label><label>{t("bootstrap.password")}<input name="password" type="password" minLength={14} required /></label>{error && <p className="form-error">{localized(error, t)}</p>}<button className="primary-button">{t("bootstrap.prepare")}</button></form></> : <><p className="muted">{t("bootstrap.saveCodes")}</p><code className="setup-secret">{setup.secret}</code><div className="recovery-grid">{setup.recovery.map((code) => <code key={code}>{code}</code>)}</div><form onSubmit={confirm}><label>{t("bootstrap.code")}<input name="totp" inputMode="numeric" pattern="[0-9]{6}" required /></label>{error && <p className="form-error">{localized(error, t)}</p>}<button className="primary-button">{t("bootstrap.activate")}</button></form></>}</section></main>;
}

function OverviewTab({ data, demo, onRestart }: { data: Overview; demo: boolean; onRestart: () => void }) {
  const { t, formatNumber, formatDuration, formatUptime, formatPlanck, formatEnum, formatItemCount } = useI18n();
  const healthy = data.node.service_state === "active" && data.chain.status === "healthy";
  const collatorVersion = data.node.version.trim() || "—";
  const demoBlocks = useMemo<LoadedSeries[]>(() => {
    const external = chartValues.map((_, index) => 9_842_610 + index * 4);
    const local = external.map((value, index) => value - (index % 7 === 0 ? 3 : index % 5 === 0 ? 1 : 0));
    return [
      { id: "guardian_local_finalized", label: t("overview.localFinalized"), metric: { __name__: "guardian_local_finalized" }, points: makeDemoPoints(local, "30m") },
      { id: "guardian_external_height", label: t("overview.externalRpc"), metric: { __name__: "guardian_external_height" }, points: makeDemoPoints(external, "30m") },
    ];
  }, [t]);
  const blocks = usePrometheusPanel("blocks", "30m", demo, demoBlocks);
  const blockSeries = useMemo<ChartSeries[]>(() => blocks.series.map((item) => {
    const external = item.id.includes("external");
    return { ...item, label: external ? t("overview.externalRpc") : t("overview.localFinalized"), color: external ? "#5bb8ff" : "#a78bfa" };
  }).sort((left) => left.id.includes("local") ? -1 : 1), [blocks.series, t]);
  const blockValues = seriesValues(blockSeries);
  const blockMin = blockValues.length ? Math.min(...blockValues) : 0;
  const blockMax = blockValues.length ? Math.max(...blockValues) : 1;
  const blockPadding = Math.max(2, Math.ceil((blockMax - blockMin) * 0.08));

  return <div className="page-content">
    <section className="hero-status">
      <div className="hero-copy"><span className={healthy ? "status-pill healthy" : "status-pill critical"}><i />{healthy ? t("overview.healthyBadge") : t("overview.attentionBadge")}</span><h2>{healthy ? t("overview.healthyTitle") : t("overview.attentionTitle")}</h2><p>{t("overview.heroText")}</p></div>
      <div className="hero-hardware" aria-hidden="true"><Image src="/beelink-mini-s.png" alt="" width={1411} height={1115} /></div>
      <div className="block-readout"><span>FINALIZED BLOCK</span><strong>{formatNumber(data.chain.local_finalized)}</strong><small>{t("overview.externalDifference", { count: data.chain.lag })}</small></div>
    </section>

    <div className="metric-grid">
      <MetricCard label="SERVICE" value={formatEnum(data.node.service_state).toUpperCase()} sub={t("overview.serviceUptime", { duration: formatUptime(data.node.uptime_seconds) })} detail={t("overview.collatorBinary", { version: collatorVersion })} accent="green" />
      <MetricCard label="PEERS" value={String(data.chain.peers)} sub={t("overview.peerThreshold")} accent="violet" />
      <MetricCard label="CPU" value={`${data.host.cpu_percent.toFixed(0)}%`} sub={t("overview.temperature", { value: data.host.temperature_c.toFixed(0) })} accent="blue" />
      <MetricCard label="MEMORY" value={`${data.host.memory_percent.toFixed(0)}%`} sub={t("overview.memoryNormal")} accent="amber" />
    </div>

    <div className="dashboard-grid">
      <section className="panel chain-panel"><PanelTitle overline="CHAIN PROGRESS" title={t("overview.chainProgress")} action={t("overview.last30m")} /><TimeSeriesChart ariaLabel={t("overview.chainChart")} series={blockSeries} loading={blocks.loading} error={blocks.error} yDomain={[Math.max(0, blockMin - blockPadding), blockMax + blockPadding]} formatValue={(value) => formatNumber(Math.round(value))} /><div className="chain-stats"><div><span>LOCAL BEST</span><strong>{formatNumber(data.chain.local_best)}</strong></div><div><span>EXTERNAL</span><strong>{formatNumber(data.chain.external_height)}</strong></div><div><span>SYNC LAG</span><strong>{data.chain.lag} blocks</strong></div></div></section>
      <section className="panel health-panel"><PanelTitle overline="HOST HEALTH" title={t("overview.hostCapacity")} /><Gauge label={t("overview.memory")} value={data.host.memory_percent} warn={90} /><Gauge label={t("overview.disk")} value={data.host.disk_percent} warn={85} /><Gauge label="CPU" value={data.host.cpu_percent} warn={90} /><div className="health-note"><span className="tiny-dot" /> {t("overview.resourceHealthy")}</div></section>
      <section className="panel reward-overview-panel"><PanelTitle overline="COLLATOR REWARDS" title={t("overview.rewardsTitle")} action={formatEnum(data.rewards.status).toUpperCase()} /><div className="reward-status-line"><span className={`reward-health ${data.rewards.status}`}><i />{data.rewards.active_session ? t("overview.activeSet") : t("overview.notActive")}</span><span>{t("overview.evidenceQuorum", { count: data.rewards.quorum })}</span></div><dl className="reward-overview-stats"><div><dt>{t("overview.lastReward")}</dt><dd>{data.rewards.last_reward_at ? t("overview.ago", { duration: formatDuration(data.rewards.seconds_since_reward) }) : t("common.collecting")}</dd></div><div><dt>{t("overview.last24h")}</dt><dd>{formatPlanck(data.rewards.reward_24h_planck)}</dd><small>{formatNumber(data.rewards.reward_24h_count)} blocks</small></div><div><dt>{t("overview.walletBalance")}</dt><dd>{formatPlanck(data.rewards.wallet_free_planck)}</dd></div></dl><p className="panel-note">{t("overview.rewardPolicy")}</p></section>
      <section className="panel automation-panel"><PanelTitle overline="REMEDIATION" title={t("overview.remediationGuard")} /><div className="guard-state"><span className="guard-icon">G</span><div><strong>{data.automation.host_locked ? t("overview.guardLocked") : data.automation.enabled ? t("overview.guardEnabled") : t("overview.guardObserve")}</strong><p>{data.automation.host_locked ? t("overview.guardLockedText") : data.automation.enabled ? t("overview.guardEnabledText") : t("overview.guardObserveText")}</p></div></div><ul className="guard-list"><li><span>AI confidence</span><b>{t("overview.minimumConfidence")}</b></li><li><span>Cooldown</span><b>{t("overview.cooldown")}</b></li><li><span>{t("overview.limitLabel")}</span><b>{t("overview.limit")}</b></li></ul><button className="outline-button danger" onClick={onRestart}>{t("overview.openRestart")}</button></section>
      <section className="panel incident-panel"><PanelTitle overline="LATEST SIGNAL" title={t("overview.latestIncident")} action={formatItemCount(data.incidents.length)} />{data.incidents.length ? data.incidents.slice(0, 2).map((item) => <IncidentRow key={item.id} item={item} />) : <Empty title={t("overview.noIncidents")} text={t("overview.noIncidentsText")} />}</section>
    </div>
  </div>;
}

function MetricsTab({ data, demo }: { data: Overview; demo: boolean }) {
  const { t, formatBlockCount } = useI18n();
  const [range, setRange] = useState("24h");
  const demoSeries = useMemo(() => ({
    cpu: [{ id: "guardian_host_cpu_percent", label: "CPU", metric: {}, points: makeDemoPoints(chartValues, range) }],
    memory: [{ id: "guardian_host_memory_percent", label: t("overview.memory"), metric: {}, points: makeDemoPoints(chartValues.map((value) => Math.min(94, value + 14)), range) }],
    peers: [{ id: "guardian_peers", label: t("metrics.peers"), metric: {}, points: makeDemoPoints(chartValues.map((value) => Math.round(value * 0.45 + 17)), range) }],
    lag: [{ id: "guardian_sync_lag", label: t("metrics.lag"), metric: {}, points: makeDemoPoints([2, 1, 0, 3, 2, 4, 1, 0, 6, 3, 2, 1, 0, 4, 2, 1, 0, 3, 2, 1, 0, 0, 5, 3, 2, 1, 0, 2, 1, 0], range) }],
  }), [range, t]);
  const cpu = usePrometheusPanel("cpu", range, demo, demoSeries.cpu);
  const memory = usePrometheusPanel("memory", range, demo, demoSeries.memory);
  const peers = usePrometheusPanel("peers", range, demo, demoSeries.peers);
  const lag = usePrometheusPanel("lag", range, demo, demoSeries.lag);

  const definitions = [
    { id: "cpu", label: t("metrics.cpu"), current: data.host.cpu_percent, state: cpu, color: "#5bb8ff", fill: true, curve: "linear" as const, thresholds: [{ value: 90, label: t("metrics.highLoad"), color: "#ff6b7a", band: "above" as const }], domain: [0, 100] as [number, number], format: (value: number) => `${value.toFixed(0)}%`, stats: (values: number[]) => [{ label: t("common.current"), value: latestValue(cpu.series) ?? data.host.cpu_percent }, { label: t("common.average"), value: average(values) }, { label: t("common.maximum"), value: values.length ? Math.max(...values) : data.host.cpu_percent }] },
    { id: "memory", label: t("metrics.memory"), current: data.host.memory_percent, state: memory, color: "#f2bd66", fill: true, curve: "linear" as const, thresholds: [{ value: 90, label: t("metrics.critical90"), color: "#ff6b7a", band: "above" as const }], domain: [0, 100] as [number, number], format: (value: number) => `${value.toFixed(0)}%`, stats: (values: number[]) => [{ label: t("common.current"), value: latestValue(memory.series) ?? data.host.memory_percent }, { label: t("common.average"), value: average(values) }, { label: t("common.maximum"), value: values.length ? Math.max(...values) : data.host.memory_percent }] },
    { id: "peers", label: t("metrics.peers"), current: data.chain.peers, state: peers, color: "#50e3a4", fill: false, curve: "step" as const, thresholds: [{ value: 3, label: t("metrics.warningBelow3"), color: "#ff6b7a", band: "below" as const }], domain: [0, Math.max(6, Math.ceil(Math.max(0, ...seriesValues(peers.series)) * 1.15))] as [number, number], format: (value: number) => `${Math.round(value)}`, stats: (values: number[]) => [{ label: t("common.current"), value: latestValue(peers.series) ?? data.chain.peers }, { label: t("common.minimum"), value: values.length ? Math.min(...values) : data.chain.peers }, { label: t("common.average"), value: average(values) }] },
    { id: "lag", label: t("metrics.lag"), current: data.chain.lag, state: lag, color: "#a78bfa", fill: false, curve: "linear" as const, scale: "symlog" as const, thresholds: [{ value: 30, label: t("metrics.warning30"), color: "#f2bd66" }, { value: 120, label: t("metrics.critical120"), color: "#ff6b7a" }], domain: [0, Math.max(120, Math.ceil(Math.max(0, ...seriesValues(lag.series)) * 1.1))] as [number, number], format: (value: number) => formatBlockCount(Math.round(value)), stats: (values: number[]) => [{ label: t("common.current"), value: latestValue(lag.series) ?? data.chain.lag }, { label: "p95", value: percentile(values, 0.95) }, { label: t("common.maximum"), value: values.length ? Math.max(...values) : data.chain.lag }] },
  ];

  return <div className="page-content"><div className="section-intro"><div><span className="eyebrow">15 SECOND SCRAPE</span><h2>{t("metrics.title")}</h2></div><select aria-label={t("metrics.range")} value={range} onChange={(event) => setRange(event.target.value)}><option value="1h">{t("metrics.range.1h")}</option><option value="24h">{t("metrics.range.24h")}</option><option value="7d">{t("metrics.range.7d")}</option><option value="30d">{t("metrics.range.30d")}</option></select></div><div className="metrics-layout">{definitions.map((panel) => {
    const values = seriesValues(panel.state.series);
    const chartSeries: ChartSeries[] = panel.state.series.map((item) => ({ ...item, label: panel.label, color: panel.color, curve: panel.curve, fill: panel.fill }));
    return <section className="panel metric-chart" key={panel.id}><PanelTitle overline="PROMETHEUS" title={panel.label} action={panel.format(latestValue(panel.state.series) ?? panel.current)} /><TimeSeriesChart ariaLabel={t("metrics.chartAria", { label: panel.label })} series={chartSeries} thresholds={panel.thresholds} loading={panel.state.loading} error={panel.state.error} yDomain={panel.domain} scale={"scale" in panel ? panel.scale : "linear"} formatValue={panel.format} summaries={panel.stats(values).map((item) => ({ label: item.label, value: panel.format(item.value) }))} /></section>;
  })}</div></div>;
}

function RewardsTab({ overview, demo, onRecover }: { overview: RewardOverview; demo: boolean; onRecover: (recovery: RewardRecovery) => void }) {
  const { t, formatNumber, formatDateTime, formatDuration, formatPlanck, formatEnum, formatItemCount, formatBlockCount } = useI18n();
  const [page, setPage] = useState<RewardPage>({ summary: overview, items: [], daily: [] });
  const [loading, setLoading] = useState(!demo);
  const [error, setError] = useState<MessageKey>();
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
    const load=async()=>{ current?.abort(); current=new AbortController(); try { const result=await api<RewardPage>("/rewards?limit=100",{signal:current.signal}); if(!stopped){setPage(result);setLoading(false);setError(undefined);} } catch(e){ if(e instanceof DOMException&&e.name==="AbortError")return; if(!stopped){setLoading(false);setError("rewards.loadError");} } };
    void load(); const timer=window.setInterval(load,15000); return()=>{stopped=true;current?.abort();window.clearInterval(timer);};
  },[demo]);

  const displayedPage = demo ? { summary: overview, items: demoItems, daily: demoDaily } : page;
  const summary = displayedPage.summary;
  const heroTitle = summary.recovery?.required
    ? t("rewards.recoveryRequired")
    : summary.status === "degraded"
      ? t("rewards.monitoringDegraded")
      : summary.active_session ? t("rewards.monitoringHealthy") : t("rewards.checkActiveSet");
  const chronological = useMemo(()=>[...displayedPage.items].sort((a,b)=>new Date(a.authored_at).getTime()-new Date(b.authored_at).getTime()),[displayedPage.items]);
  const intervals = useMemo<ChartSeries[]>(()=>{
    const points=chronological.slice(1).map((item,index)=>({timestamp:new Date(item.authored_at).getTime()/1000,value:(new Date(item.authored_at).getTime()-new Date(chronological[index].authored_at).getTime())/1000}));
    return points.length?[{id:"reward_interval",label:t("rewards.interval"),metric:{},points,color:"#50e3a4"}]:[];
  },[chronological,t]);
  const cumulativeDemo = useMemo<LoadedSeries[]>(()=>[{id:"guardian_collator_reward_sdn_total",label:t("rewards.cumulative"),metric:{__name__:"guardian_collator_reward_sdn_total"},points:makeDemoPoints(chartValues.map((_,i)=>810+i*2),"24h")}],[t]);
  const prometheus=usePrometheusPanel("rewards","24h",demo,cumulativeDemo);
  const cumulative=useMemo<ChartSeries[]>(()=>prometheus.series.filter(item=>item.id.includes("reward_sdn_total")).map(item=>({...item,label:t("rewards.cumulative"),color:"#a78bfa",fill:true})),[prometheus.series,t]);
  const cumulativeValues=seriesValues(cumulative); const cumulativeMin=cumulativeValues.length?Math.min(...cumulativeValues):0; const cumulativeMax=cumulativeValues.length?Math.max(...cumulativeValues):1;
  const intervalValues=seriesValues(intervals); const intervalMax=Math.max(1800,...intervalValues);

  return <div className="page-content reward-page">
    <div className="section-intro"><div><span className="eyebrow">ON-CHAIN REWARD PROOF</span><h2>{t("rewards.title")}</h2><p className="section-copy">{t("rewards.description")}</p></div><a className="outline-button reward-link" href={`https://shiden.subscan.io/account/${summary.address}`} target="_blank" rel="noreferrer">{t("rewards.openSubscan")}</a></div>
    <section className={`reward-hero ${summary.status}`}><div><span className={`reward-health ${summary.status}`}><i />{formatEnum(summary.status).toUpperCase()}</span><h3>{heroTitle}</h3><p>{summary.last_reward_at ? t("rewards.lastConfirmed", { time: formatDateTime(summary.last_reward_at), duration: formatDuration(summary.seconds_since_reward) }) : t("rewards.waitingFirst")}</p></div><div className="reward-address"><span>REWARD WALLET</span><code title={summary.address}>{summary.address}</code><small>runtime spec {summary.spec_version || "—"} · quorum {summary.quorum}/3</small></div></section>
    <div className="reward-card-grid">
      <MetricCard label="ACTIVE SET" value={summary.active_session ? t("rewards.inSet", { count: summary.validator_count }) : t("rewards.notApplicable")} sub={summary.active_session ? t("rewards.currentSession") : t("rewards.needsAttention")} accent={summary.active_session ? "green" : "amber"} />
      <MetricCard label="LAST REWARD" value={summary.last_reward_at ? formatDuration(summary.seconds_since_reward) : t("common.collecting")} sub={`block #${formatNumber(summary.last_authored_block || 0)}`} accent="violet" />
      <MetricCard label="24 HOURS" value={formatPlanck(summary.reward_24h_planck)} sub={formatBlockCount(summary.reward_24h_count)} accent="blue" />
      <MetricCard label="WALLET" value={formatPlanck(summary.wallet_free_planck)} sub={t("overview.walletBalance")} accent="amber" />
    </div>
    {(summary.gap||!summary.schema_ok||summary.quorum<2)&&<div className="reward-warning" role="status"><strong>{summary.recovery?.required?t("rewards.prunedWarning"):t("rewards.warningEvidence")}</strong>{summary.gap&&<small>{summary.gap}</small>}{summary.recovery?.required&&<button className="primary-button" disabled={demo} onClick={()=>onRecover(summary.recovery!)}>{t("rewards.recoveryAction")}</button>}</div>}
    {summary.last_history_gap&&<div className="reward-history-gap" role="status">{t("rewards.historyGap",{from:formatNumber(summary.last_history_gap.from_block),through:formatNumber(summary.last_history_gap.through_block),resume:formatNumber(summary.last_history_gap.resume_from_block),actor:summary.last_history_gap.acknowledged_by})}</div>}
    <div className="metrics-layout reward-charts">
      <section className="panel metric-chart"><PanelTitle overline="PROMETHEUS" title={t("rewards.cumulative")} action={formatPlanck(summary.reward_total_planck)} /><TimeSeriesChart ariaLabel={t("rewards.cumulativeChart")} series={cumulative} loading={prometheus.loading} error={prometheus.error} yDomain={[Math.max(0,cumulativeMin-(cumulativeMax-cumulativeMin)*.1),cumulativeMax+(cumulativeMax-cumulativeMin||1)*.1]} formatValue={(value)=>`${value.toFixed(3)} SDN`} /></section>
      <section className="panel metric-chart"><PanelTitle overline="AUTHORSHIP" title={t("rewards.interval")} action={summary.last_reward_at?formatDuration(summary.seconds_since_reward):t("common.collecting")} /><TimeSeriesChart ariaLabel={t("rewards.intervalChart")} series={intervals} loading={loading} error={error} thresholds={[{value:900,label:t("rewards.warning15m"),color:"#f2bd66"},{value:1800,label:t("rewards.critical30m"),color:"#ff6b7a"}]} yDomain={[0,intervalMax]} formatValue={(value)=>formatDuration(Math.round(value))} /></section>
    </div>
    <section className="panel reward-daily"><PanelTitle overline="DAILY TOTAL" title={t("rewards.daily")} action="JST" /><DailyRewardBars items={displayedPage.daily||[]} /></section>
    <section className="panel reward-ledger"><PanelTitle overline="VERIFIED LEDGER" title={t("rewards.recent")} action={formatItemCount(displayedPage.items.length)} />{displayedPage.items.length?<div className="reward-table"><div className="reward-table-head"><span>BLOCK</span><span>JST</span><span>{t("rewards.confirmedAmount")}</span><span>{t("rewards.verification")}</span><span>{t("common.evidence")}</span></div>{displayedPage.items.map(item=><div className="reward-table-row" key={item.block_number}><a href={`https://shiden.subscan.io/block/${item.block_number}`} target="_blank" rel="noreferrer">#{formatNumber(item.block_number)}</a><time>{formatDateTime(item.authored_at)}</time><span title={`${item.credited_planck} Planck`}>{formatPlanck(item.credited_planck)}</span><b className={`verification ${item.verification}`}>{formatEnum(item.verification)}</b><span>{item.source_count}/3</span></div>)}</div>:<Empty title={t("rewards.collectingHistory")} text={t("rewards.collectingHistoryText")} />}</section>
  </div>;
}

function DailyRewardBars({items}:{items:RewardDaily[]}) {
  const { t, formatDate, formatBlockCount } = useI18n();
  if(!items.length)return <Empty title={t("rewards.collectingDaily")} text={t("rewards.collectingDailyText")} />;
  const values=items.map(item=>Number(BigInt(item.amount_planck||"0"))/1e18); const max=Math.max(...values,1);
  return <div className="daily-bars" role="img" aria-label={t("rewards.dailyAria")}><div className="daily-bars-plot">{items.map((item,index)=><div className="daily-bar-item" key={item.day}><span className="daily-value">{values[index].toFixed(1)}</span><div className="daily-bar-track"><i style={{height:`${Math.max(2,values[index]/max*100)}%`}} /></div><time>{formatDate(`${item.day}T00:00:00+09:00`)}</time><small>{formatBlockCount(item.count)}</small></div>)}</div></div>;
}

function LogsTab({ lines, demo }: { lines: LogLine[]; demo: boolean }) {
  const { t, formatChartTime, formatEnum } = useI18n();
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
  return <div className="page-content"><div className="section-intro"><div><span className="eyebrow">SYSTEMD JOURNAL</span><h2>{t("logs.title")}</h2></div><div className="log-actions"><input placeholder={t("logs.search")} aria-label={t("logs.search")} value={query} onChange={(event) => setQuery(event.target.value)} /><select aria-label={t("logs.priority")} value={priority} onChange={(event) => setPriority(event.target.value)}><option value="">{t("logs.all")}</option><option value="warning">{t("logs.warning")}</option><option value="error">{t("logs.error")}</option></select></div></div><section className="log-viewer"><div className="log-head"><span>JST</span><span>LEVEL</span><span>MESSAGE</span></div>{shown.map((line) => <div className="log-line" key={line.cursor}><time>{formatChartTime(new Date(line.timestamp).getTime() / 1000)}</time><span className={`level ${line.priority}`}>{formatEnum(line.priority)}</span><code>{line.message}</code></div>)}{!shown.length && <Empty title={t("logs.empty")} text={t("logs.emptyText")} />}</section></div>;
}

const incidentMessageKeys: Record<string, MessageKey> = {
  "service-inactive": "incident.service-inactive",
  "block-stalled": "incident.block-stalled",
  "peers-zero": "incident.peers-zero",
  "peers-low": "incident.peers-low",
  "disk-critical": "incident.disk-critical",
  "disk-warning": "incident.disk-warning",
  "memory-high": "incident.memory-high",
  "log-oom": "incident.log-oom",
  "log-panic": "incident.log-panic",
  "log-database": "incident.log-database",
  "reward-active-set-missing": "incident.reward-active-set-missing",
  "reward-credit-mismatch": "incident.reward-credit-mismatch",
};

function incidentTitle(item: Incident, t: (key: MessageKey, values?: MessageValues) => string) {
  const fingerprint = item.fingerprint || "";
  if (fingerprint.startsWith("manual-diagnosis-")) return t("incident.manual-diagnosis");
  if (fingerprint === "sync-lag-critical" || fingerprint === "sync-lag-warning") {
    const lag = typeof item.evidence?.lag === "number" ? item.evidence.lag : "—";
    return t(fingerprint === "sync-lag-critical" ? "incident.sync-lag-critical" : "incident.sync-lag-warning", { lag });
  }
  if (fingerprint === "reward-monitor-degraded") return t(item.evidence?.error ? "incident.reward-monitor-degraded.database" : "incident.reward-monitor-degraded.evidence");
  if (fingerprint === "reward-silence") return t(item.severity === "critical" ? "incident.reward-silence.critical" : "incident.reward-silence.warning");
  const key = incidentMessageKeys[fingerprint];
  return key ? t(key) : item.title;
}

function IncidentsTab({ incidents, demo, onDiagnose }: { incidents: Incident[]; demo: boolean; onDiagnose: () => void }) {
  const { t, formatDateTime, formatEnum } = useI18n();
  return <div className="page-content"><div className="section-intro"><div><span className="eyebrow">EVIDENCE-BASED TRIAGE</span><h2>{t("incidents.title")}</h2></div><button className="outline-button" onClick={onDiagnose}>{t("incidents.runDiagnosis")}</button></div><section className="panel incident-table"><div className="table-head"><span>{t("incidents.severity")}</span><span>{t("incidents.details")}</span><span>{t("common.status")}</span><span>{t("incidents.opened")}</span></div>{incidents.map((item) => <div className="table-row" key={item.id}><span><b className={`severity ${item.severity}`}>{formatEnum(item.severity)}</b></span><span><strong>{incidentTitle(item, t)}</strong><small>{item.diagnosis || t("incidents.pending")}</small></span><span>{formatEnum(item.status)}</span><time>{formatDateTime(item.opened_at)}</time></div>)}{!incidents.length && <Empty title={t("incidents.empty")} text={t("incidents.emptyText")} />}{demo && <p className="demo-foot">{t("incidents.demo")}</p>}</section></div>;
}

function MaintenanceTab({ data, onRestart }: { data: Overview; onRestart: () => void }) {
  const { t, formatEnum } = useI18n();
  return <div className="page-content"><div className="maintenance-hero"><div><span className="eyebrow">CONTROLLED ACTIONS ONLY</span><h2>{t("maintenance.title")}</h2><p>{t("maintenance.description")}</p></div><div className="lock-badge">LOCKED SCOPE</div></div><div className="maintenance-grid"><section className="panel"><PanelTitle overline="TARGET" title="astar.service" /><dl className="detail-list"><div><dt>{t("common.state")}</dt><dd className="good">{formatEnum(data.node.service_state)}</dd></div><div><dt>{t("maintenance.restartCount")}</dt><dd>{data.node.restart_count}</dd></div><div><dt>Cooldown</dt><dd>{t("overview.cooldown")}</dd></div><div><dt>{t("maintenance.past24h")}</dt><dd>0 / 2</dd></div></dl><button className="primary-button danger-fill" onClick={onRestart}>{t("maintenance.requestRestart")}</button></section><section className="panel"><PanelTitle overline="HARD GUARDS" title={t("maintenance.guardTitle")} /><ul className="check-list"><li>{t("maintenance.guard.lock")}</li><li>{t("maintenance.guard.cooldown")}</li><li>{t("maintenance.guard.limit")}</li><li>{t("maintenance.guard.recovery")}</li><li>{t("maintenance.guard.idempotency")}</li></ul><p className="panel-note">{t("maintenance.guardNote")}</p></section></div></div>;
}

function auditError(item: Audit) {
  const value = item.details?.error;
  return typeof value === "string" && value ? value.slice(0, 320) : "";
}

function SettingsTab({ data, audits, demo, setNotice, onAuditRefresh, onAutomation }: { data: Overview; audits: Audit[]; demo: boolean; setNotice: (x: MessageState) => void; onAuditRefresh: () => Promise<void>; onAutomation: () => void }) {
  const { t, formatDateTime, formatEnum } = useI18n();
  const sample: Audit[] = [{ id: "a1", action: "session.login", actor: "admin", created_at: new Date().toISOString(), result: "success" }];
  const [testing, setTesting] = useState<"" | "smtp" | "gemini">("");
  async function testIntegration(kind: "smtp" | "gemini") {
    if (demo) { setNotice(message("settings.previewNoExternal")); return; }
    setTesting(kind);
    try {
      await api(`/settings/test-${kind}`, { method: "POST", body: "{}" });
      setNotice(message("settings.testQueued", { service: kind === "smtp" ? "SMTP" : "Gemini" }));
      await onAuditRefresh();
    } catch {
      setNotice(message("settings.testFailed"));
    } finally {
      setTesting("");
    }
  }
  return <div className="page-content"><div className="settings-grid"><section className="panel"><PanelTitle overline="AUTOMATION" title={t("settings.automation")} /><div className="setting-row"><div><strong>{t("settings.observation")}</strong><p>{t("settings.observationText")}</p></div><div><span className="state-chip">{data.automation.host_locked ? t("settings.hostLocked") : formatEnum(data.automation.mode)}</span><button className="small-button" onClick={onAutomation}>{data.automation.enabled ? t("settings.disable") : t("settings.enable")}</button></div></div><div className="setting-row"><div><strong>{t("settings.email")}</strong><p>{t("settings.emailText")}</p></div><button className="small-button" disabled={testing === "smtp"} onClick={() => testIntegration("smtp")}>{testing === "smtp" ? t("settings.registering") : t("settings.test")}</button></div><div className="setting-row"><div><strong>Gemini API</strong><p>{t("settings.geminiText")}</p></div><button className="small-button" disabled={testing === "gemini"} onClick={() => testIntegration("gemini")}>{testing === "gemini" ? t("settings.registering") : t("settings.test")}</button></div></section><section className="panel audit-panel"><PanelTitle overline="AUDIT TRAIL" title={t("settings.audit")} />{(demo ? sample : audits).map((item) => { const error = auditError(item); return <div className="audit-row" key={item.id}><span className="audit-mark">A</span><div><strong>{item.action}</strong><small>{item.actor} · {formatDateTime(item.created_at)}</small>{error && <small className="audit-error">{error}</small>}</div><b className={`audit-result ${item.result}`}>{formatEnum(item.result)}</b></div>; })}</section></div></div>;
}

function AutomationDialog({ enabled, onClose, onDone }: { enabled: boolean; onClose: () => void; onDone: (x: MessageState) => void }) {
  const { t } = useI18n();
  const [error, setError] = useState<MessageState>(null);
  async function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form = new FormData(event.currentTarget); try { await api("/settings", { method: "PUT", body: JSON.stringify({ automation_enabled: !enabled, password: form.get("password"), totp_code: form.get("totp") }) }); onDone(message(enabled ? "automation.saved.disable" : "automation.saved.enable")); } catch { setError(message(enabled ? "automation.error.disable" : "automation.error.enable")); } }
  return <div className="modal-backdrop" role="presentation"><section className="modal" role="dialog" aria-modal="true" aria-labelledby="automation-title"><button className="modal-close" aria-label={t("common.close")} onClick={onClose}>×</button><LanguageToggle className="modal-language-toggle" /><span className="eyebrow">STEP-UP AUTHENTICATION</span><h2 id="automation-title">{t(enabled ? "automation.title.disable" : "automation.title.enable")}</h2><p>{t(enabled ? "automation.description.disable" : "automation.description.enable")}</p><form onSubmit={submit}><label>{t("common.password")}<input name="password" type="password" required /></label><label>{t("common.authCode")}<input name="totp" inputMode="numeric" pattern="[0-9]{6}" required /></label>{error && <p className="form-error">{localized(error, t)}</p>}<div className="modal-actions"><button type="button" className="outline-button" onClick={onClose}>{t("common.cancel")}</button><button type="submit" className="primary-button">{t("automation.save")}</button></div></form></section></div>;
}

function RestartDialog({ demo, onClose, onDone }: { demo: boolean; onClose: () => void; onDone: (x: MessageState) => void }) {
  const { t } = useI18n();
  const [error, setError] = useState<MessageState>(null);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    if (form.get("confirm") !== "tk_sdn_collator") { setError(message("restart.error.confirm")); return; }
    if (demo) { onDone(message("restart.preview")); return; }
    try { await api("/actions/restart", { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ password: form.get("password"), totp_code: form.get("totp"), reason: form.get("reason"), confirm: form.get("confirm") }) }); onDone(message("restart.accepted")); } catch (e) { setError(e instanceof Error && e.message ? e.message : message("restart.error.generic")); }
  }
  return <div className="modal-backdrop" role="presentation"><section className="modal" role="dialog" aria-modal="true" aria-labelledby="restart-title"><button className="modal-close" aria-label={t("common.close")} onClick={onClose}>×</button><LanguageToggle className="modal-language-toggle" /><span className="eyebrow">STEP-UP AUTHENTICATION</span><h2 id="restart-title">{t("restart.title")}</h2><p>{t("restart.description")}</p><form onSubmit={submit}><label>{t("restart.reason")}<textarea name="reason" minLength={10} required placeholder={t("restart.reasonPlaceholder")} /></label><label>{t("common.password")}<input name="password" type="password" required /></label><label>{t("common.authCode")}<input name="totp" inputMode="numeric" pattern="[0-9]{6}" required /></label><label>{t("restart.confirmLabel")}<input name="confirm" placeholder="tk_sdn_collator" required /></label>{error && <p className="form-error">{localized(error, t)}</p>}<div className="modal-actions"><button type="button" className="outline-button" onClick={onClose}>{t("common.cancel")}</button><button type="submit" className="primary-button danger-fill">{t("restart.submit")}</button></div></form></section></div>;
}

function RewardRecoveryDialog({ recovery, onClose, onDone }: { recovery: RewardRecovery; onClose: () => void; onDone: (x: MessageState) => void }) {
  const { t, formatNumber } = useI18n();
  const [error, setError] = useState<MessageState>(null);
  const [submitting, setSubmitting] = useState(false);
  const confirmation = `PRUNED GAP ${recovery.expected_cursor}`;
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    if (form.get("confirm") !== confirmation) { setError(message("rewardRecovery.error.confirm", { confirmation })); return; }
    setSubmitting(true);
    try {
      await api("/actions/reward-gap/acknowledge", { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ password: form.get("password"), totp_code: form.get("totp"), reason: form.get("reason"), confirm: form.get("confirm"), expected_cursor: recovery.expected_cursor }) });
      onDone(message("rewardRecovery.accepted"));
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : message("rewardRecovery.error.generic"));
      setSubmitting(false);
    }
  }
  return <div className="modal-backdrop" role="presentation"><section className="modal" role="dialog" aria-modal="true" aria-labelledby="reward-recovery-title"><button className="modal-close" aria-label={t("common.close")} onClick={onClose}>×</button><LanguageToggle className="modal-language-toggle" /><span className="eyebrow">STEP-UP AUTHENTICATION</span><h2 id="reward-recovery-title">{t("rewardRecovery.title")}</h2><p>{t("rewardRecovery.description",{from:formatNumber(recovery.expected_cursor+1),resume:formatNumber(recovery.candidate_resume_from_block)})}</p><p className="recovery-data-warning">{t("rewardRecovery.dataWarning")}</p><form onSubmit={submit}><label>{t("rewardRecovery.reason")}<textarea name="reason" minLength={10} maxLength={1000} required placeholder={t("rewardRecovery.reasonPlaceholder")} /></label><label>{t("common.password")}<input name="password" type="password" required /></label><label>{t("common.authCode")}<input name="totp" inputMode="numeric" pattern="[0-9]{6}" required /></label><label>{t("rewardRecovery.confirmLabel",{confirmation})}<input name="confirm" autoComplete="off" required /></label>{error&&<p className="form-error">{localized(error,t)}</p>}<div className="modal-actions"><button type="button" className="outline-button" disabled={submitting} onClick={onClose}>{t("common.cancel")}</button><button type="submit" className="primary-button" disabled={submitting}>{submitting?t("rewardRecovery.submitting"):t("rewardRecovery.submit")}</button></div></form></section></div>;
}

function MetricCard({ label, value, sub, detail, accent }: { label: string; value: string; sub: string; detail?: string; accent: string }) { return <article className={`metric-card ${accent}`}><span>{label}</span><strong>{value}</strong><small>{sub}</small>{detail && <small className="metric-detail" title={detail}>{detail}</small>}</article>; }
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

function TimeSeriesChart({ ariaLabel, series, thresholds = [], loading, error, yDomain, scale = "linear", formatValue, summaries = [] }: { ariaLabel: string; series: ChartSeries[]; thresholds?: ChartThreshold[]; loading: boolean; error?: MessageKey; yDomain: [number, number]; scale?: "linear" | "symlog"; formatValue: (value: number) => string; summaries?: { label: string; value: string }[] }) {
  const { t, formatChartTime } = useI18n();
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

  if (loading && !allPoints.length) return <div className="chart-state" role="status"><span className="chart-spinner" />{t("chart.loading")}</div>;
  if (!allPoints.length) return <div className="chart-state chart-state-error" role="status"><strong>{t("chart.empty")}</strong><span>{error ? t(error) : t("chart.emptyText")}</span></div>;

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
    {primary?.points.length === 1 && <p className="chart-note">{t("chart.collecting")}</p>}
    {error && <p className="chart-note chart-note-error">{t(error)}</p>}
    {summaries.length > 0 && <dl className="chart-summary">{summaries.map((item) => <div key={item.label}><dt>{item.label}</dt><dd>{item.value}</dd></div>)}</dl>}
  </div>;
}

function Gauge({ label, value, warn }: { label: string; value: number; warn: number }) { return <div className="gauge"><div><span>{label}</span><b>{value.toFixed(0)}%</b></div><div className="gauge-track"><i className={value >= warn ? "warn" : ""} style={{ width: `${value}%` }} /></div></div>; }
function IncidentRow({ item }: { item: Incident }) { const { t, formatDateTime, formatEnum } = useI18n(); return <div className="incident-row"><b className={`severity ${item.severity}`}>{formatEnum(item.severity)}</b><div><strong>{incidentTitle(item, t)}</strong><small>{item.diagnosis}</small></div><time>{formatDateTime(item.opened_at)}</time></div>; }
function Empty({ title, text }: { title: string; text: string }) { return <div className="empty"><span>—</span><strong>{title}</strong><p>{text}</p></div>; }
