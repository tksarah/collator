import type { Metadata } from "next";
import "./globals.css";

const siteUrl = process.env.NEXT_PUBLIC_SITE_URL ?? "https://guardian.invalid";

export const metadata: Metadata = {
  metadataBase: new URL(siteUrl),
  title: "Shiden Guardian | tk_sdn_collator",
  description: "Shiden Collatorの状態・ログ・インシデントを安全に監視する運用ダッシュボード",
  openGraph: {
    title: "Shiden Guardian | tk_sdn_collator",
    description: "Shiden Collatorの状態・ログ・インシデントを安全に監視する運用ダッシュボード",
    type: "website",
    locale: "ja_JP",
    images: [{ url: "/og.png", width: 1536, height: 1024, alt: "Shiden Guardian node operations dashboard" }],
  },
  twitter: {
    card: "summary_large_image",
    title: "Shiden Guardian | tk_sdn_collator",
    description: "Shiden Collatorの安全な監視・診断・復旧ダッシュボード",
    images: ["/og.png"],
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="ja">
      <body>{children}</body>
    </html>
  );
}
