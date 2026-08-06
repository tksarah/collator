import type { Metadata } from "next";
import "./globals.css";

const siteUrl = process.env.NEXT_PUBLIC_SITE_URL ?? "https://guardian.invalid";

export const metadata: Metadata = {
  metadataBase: new URL(siteUrl),
  title: "Shiden Guardian | tk_sdn_collator",
  description: "A secure operations dashboard for monitoring Shiden Collator status, logs, and incidents",
  openGraph: {
    title: "Shiden Guardian | tk_sdn_collator",
    description: "A secure operations dashboard for monitoring Shiden Collator status, logs, and incidents",
    type: "website",
    locale: "en_US",
    alternateLocale: ["ja_JP"],
    images: [{ url: "/og.png", width: 1536, height: 1024, alt: "Shiden Guardian node operations dashboard" }],
  },
  twitter: {
    card: "summary_large_image",
    title: "Shiden Guardian | tk_sdn_collator",
    description: "Secure monitoring, diagnosis, and remediation for a Shiden Collator",
    images: ["/og.png"],
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
