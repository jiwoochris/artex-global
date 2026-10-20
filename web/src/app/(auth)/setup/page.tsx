"use client";

import { LanguageSelector } from "@/i18n/language-selector";
import { useI18n } from "@/i18n";
import { useEffect, useState } from "react";

import { useRouter } from "next/navigation";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function SetupPage() {
  "use no memo";
  const { t: swt, rich } = useI18n();

  const router = useRouter();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [setupToken, setSetupToken] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [unavailable, setUnavailable] = useState(false);
  const [tokenRequired, setTokenRequired] = useState(false);

  useEffect(() => {
    api.authStatus()
      .then(({ initialized, setup_token_required }) => {
        if (initialized) router.replace("/login");
        else setTokenRequired(setup_token_required !== false);
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : swt("interface.m0001"));
        setUnavailable(true);
      })
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setError(swt("interface.m0041"));
      return;
    }
    if (password.length < 8) {
      setError(swt("interface.m0042"));
      return;
    }
    if (tokenRequired && !setupToken.trim()) {
      setError(swt("settings.setupTokenRequired"));
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.initPassword(password, setupToken.trim() || undefined);
      auth.setToken(token);
      router.replace("/function/tasks");
    } catch (err) {
      setError(err instanceof Error ? err.message : swt("interface.m0043"));
    } finally {
      setLoading(false);
    }
  }

  if (checking) return null;

  return (
    <div className="relative flex min-h-dvh">
      <div className="absolute right-4 top-4 z-10"><LanguageSelector /></div>
      {/* Left panel */}
      <div className="hidden flex-col items-center justify-center bg-primary p-12 text-center lg:flex lg:w-1/3">
        <div className="relative flex items-center justify-center">
          <div className="absolute size-80 rounded-full border border-primary-foreground/10" />
          <div className="absolute size-60 rounded-full border border-primary-foreground/15" />
          <div className="absolute size-40 rounded-full border border-primary-foreground/20" />
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src="/scopeweaver.svg"
            alt="ScopeWeaver"
            width={160}
            height={160}
            className="relative"
          />
        </div>
      </div>

      {/* Right panel */}
      <div className="flex w-full items-center justify-center bg-background p-8 lg:w-2/3">
        <div className="w-full max-w-md space-y-10 py-24 lg:py-32">
          <div className="space-y-4 text-center">
            <h2 className="text-2xl font-medium tracking-tight">{unavailable ? swt("settings.setupUnavailableTitle") : swt("interface.m0044")}</h2>
            <p className="mx-auto max-w-xl text-muted-foreground">{unavailable ? swt("settings.setupUnavailableHelp") : swt("interface.m0045")}</p>
          </div>
          {unavailable ? (
            <div className="flex flex-col gap-4">
              {error && <p className="text-center text-sm text-destructive">{error}</p>}
              <Button type="button" className="w-full" onClick={() => window.location.reload()}>
                {swt("common.retry")}
              </Button>
            </div>
          ) : (
            <form onSubmit={handleSubmit} className="flex flex-col gap-4">
              {tokenRequired ? (
                <div className="space-y-1.5">
                  <Label htmlFor="setup-token">{swt("settings.setupTokenLabel")}</Label>
                  <Input
                    id="setup-token"
                    type="password"
                    value={setupToken}
                    onChange={(e) => setSetupToken(e.target.value)}
                    placeholder={swt("settings.setupTokenPlaceholder")}
                    autoComplete="off"
                    spellCheck={false}
                  />
                  <p className="text-muted-foreground text-xs">
                    {rich("settings.setupTokenHelp", {}, { code: (text) => <code className="font-mono">{text}</code> })}
                  </p>
                </div>
              ) : null}
              <div className="space-y-1.5">
                <Label htmlFor="password">{swt("interface.m0046")}</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder={swt("interface.m0047")}
                  autoFocus
                  autoComplete="new-password"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="confirm">{swt("interface.m0048")}</Label>
                <Input
                  id="confirm"
                  type="password"
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  placeholder={swt("interface.m0049")}
                  autoComplete="new-password"
                />
              </div>
              {error && <p className="text-sm text-destructive">{error}</p>}
              <Button type="submit" className="w-full" disabled={loading || !password || !confirm}>
                {loading ? swt("interface.m0050") : swt("interface.m0051")}
              </Button>
            </form>
          )}
        </div>
      </div>
    </div>
  );
}
