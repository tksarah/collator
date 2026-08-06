"use client";

import { createContext, ReactNode, useCallback, useContext, useEffect, useMemo, useState } from "react";
import en from "./locales/en.json";
import ja from "./locales/ja.json";

export type Locale = "en" | "ja";
export type MessageKey = keyof typeof en;
export type MessageValues = Record<string, string | number>;
export type LocalizedMessage = { key: MessageKey; values?: MessageValues };

const DEFAULT_LOCALE: Locale = "en";
export const LOCALE_STORAGE_KEY = "sg_locale";
const catalogs: Record<Locale, Record<MessageKey, string>> = { en, ja };

type I18nContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: MessageKey, values?: MessageValues) => string;
  formatNumber: (value: number | bigint) => string;
  formatDateTime: (value: string | number | Date) => string;
  formatDate: (value: string | number | Date) => string;
  formatChartTime: (timestamp: number, includeDate?: boolean) => string;
  formatDuration: (seconds: number) => string;
  formatUptime: (seconds: number) => string;
  formatPlanck: (value?: string, digits?: number) => string;
  formatEnum: (value: string) => string;
  formatItemCount: (count: number) => string;
  formatBlockCount: (count: number) => string;
};

const I18nContext = createContext<I18nContextValue | null>(null);

function isLocale(value: string | null): value is Locale {
  return value === "en" || value === "ja";
}

function interpolate(message: string, values?: MessageValues) {
  if (!values) return message;
  return message.replace(/\{([A-Za-z0-9_]+)\}/g, (token, key: string) => key in values ? String(values[key]) : token);
}

function LocaleLoading() {
  return <main className="login-shell" aria-busy="true"><section className="login-card locale-loading"><div className="brand-orbit large"><span /></div><div className="brand"><div><strong>SHIDEN</strong><small>GUARDIAN</small></div></div></section></main>;
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(DEFAULT_LOCALE);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    let cancelled = false;
    window.queueMicrotask(() => {
      if (cancelled) return;
      let next = DEFAULT_LOCALE;
      try {
        const saved = window.localStorage.getItem(LOCALE_STORAGE_KEY);
        if (isLocale(saved)) next = saved;
      } catch {
        // Storage can be unavailable in hardened browser contexts.
      }
      setLocaleState(next);
      document.documentElement.lang = next;
      setReady(true);
    });
    return () => { cancelled = true; };
  }, []);

  const setLocale = useCallback((next: Locale) => {
    setLocaleState(next);
    document.documentElement.lang = next;
    try {
      window.localStorage.setItem(LOCALE_STORAGE_KEY, next);
    } catch {
      // Keep the selected locale in memory when persistence is unavailable.
    }
  }, []);

  const t = useCallback((key: MessageKey, values?: MessageValues) => interpolate(catalogs[locale][key], values), [locale]);
  const intlLocale = locale === "ja" ? "ja-JP" : "en-US";

  const value = useMemo<I18nContextValue>(() => ({
    locale,
    setLocale,
    t,
    formatNumber: (number) => new Intl.NumberFormat(intlLocale).format(number),
    formatDateTime: (date) => new Intl.DateTimeFormat(intlLocale, { timeZone: "Asia/Tokyo", month: locale === "ja" ? "2-digit" : "short", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(date)),
    formatDate: (date) => new Intl.DateTimeFormat(intlLocale, { timeZone: "Asia/Tokyo", month: locale === "ja" ? "numeric" : "short", day: "numeric" }).format(new Date(date)),
    formatChartTime: (timestamp, includeDate = false) => new Intl.DateTimeFormat(intlLocale, { timeZone: "Asia/Tokyo", ...(includeDate ? { month: locale === "ja" ? "2-digit" as const : "short" as const, day: "2-digit" as const } : {}), hour: "2-digit", minute: "2-digit" }).format(new Date(timestamp * 1000)),
    formatDuration: (seconds) => {
      if (seconds < 60) return t("common.duration.seconds", { count: seconds });
      if (seconds < 3600) return t("common.duration.minutes", { count: Math.floor(seconds / 60) });
      return t("common.duration.hours", { hours: Math.floor(seconds / 3600), minutes: Math.floor((seconds % 3600) / 60) });
    },
    formatUptime: (seconds) => t("common.uptime", { days: Math.floor(seconds / 86400), hours: Math.floor((seconds % 86400) / 3600) }),
    formatPlanck: (rawValue, digits = 6) => {
      try {
        const raw = BigInt(rawValue || "0");
        const base = BigInt(10) ** BigInt(18);
        const whole = raw / base;
        const fraction = (raw % base).toString().padStart(18, "0").slice(0, digits).replace(/0+$/, "");
        return `${new Intl.NumberFormat(intlLocale).format(whole)}${fraction ? `.${fraction}` : ""} SDN`;
      } catch {
        return "—";
      }
    },
    formatEnum: (raw) => {
      const key = `enum.${raw}` as MessageKey;
      return key in catalogs[locale] ? catalogs[locale][key] : raw;
    },
    formatItemCount: (count) => t(locale === "en" && count === 1 ? "common.item" : "common.items", { count: new Intl.NumberFormat(intlLocale).format(count) }),
    formatBlockCount: (count) => t(locale === "en" && count === 1 ? "common.block" : "common.blocks", { count: new Intl.NumberFormat(intlLocale).format(count) }),
  }), [intlLocale, locale, setLocale, t]);

  return <I18nContext.Provider value={value}>
    <div className={ready ? "locale-gate ready" : "locale-gate"}>{children}</div>
    {!ready && <LocaleLoading />}
  </I18nContext.Provider>;
}

export function useI18n() {
  const value = useContext(I18nContext);
  if (!value) throw new Error("useI18n must be used within I18nProvider");
  return value;
}

export function LanguageToggle({ className = "" }: { className?: string }) {
  const { locale, setLocale, t } = useI18n();
  return <div className={`language-toggle ${className}`.trim()} role="group" aria-label={t("language.label")}>
    <button type="button" aria-pressed={locale === "en"} onClick={() => setLocale("en")}>{t("language.english")}</button>
    <button type="button" aria-pressed={locale === "ja"} onClick={() => setLocale("ja")}>{t("language.japanese")}</button>
  </div>;
}
