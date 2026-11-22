import { useState } from "react";
import { Check, KeyRound, Minus } from "lucide-react";
import { put } from "../api";
import { localTime } from "../format";
import type { Feature, LicenseStatus } from "../types";
import { errMsg, featureTitles, Field, Note, StateBadge, useConfirm, useMe, useRefreshMe, useToast } from "../ui";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";

const stateText: Record<LicenseStatus["state"], [string, "on" | "off" | "idle" | "warn"]> = {
  none: ["Free core", "idle"],
  active: ["Active", "on"],
  grace: ["Expired — grace period", "warn"],
  expired: ["Expired", "off"],
  invalid: ["Not valid", "off"],
};

export default function License() {
  const me = useMe();
  const refreshMe = useRefreshMe();
  const toast = useToast();
  const confirm = useConfirm();
  const lic = me.license;
  const [key, setKey] = useState("");
  const [label, kind] = stateText[lic.state];
  const install = (k: string) =>
    put("/admin/api/license", { key: k })
      .then(() => {
        setKey("");
        refreshMe();
        toast(k ? "License installed" : "License removed");
      })
      .catch((e) => toast(errMsg(e), true));
  const remove = async () => {
    if (await confirm({ title: "Remove the license?", description: "Paid features turn off at once.", action: "Remove license", danger: true })) install("");
  };
  return (
    <section className="grid gap-4">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center gap-3">
            <StateBadge kind={kind}>{label}</StateBadge>
            {lic.customer && (
              <span className="text-sm">
                <b>{lic.customer}</b> · {lic.plan}
              </span>
            )}
            <span className="flex-1" />
            {lic.expires_at && <span className="text-sm text-muted-foreground">expires {localTime(lic.expires_at)}</span>}
          </div>
        </CardHeader>
        <CardContent className="grid gap-4">
          {lic.error && (
            <Alert variant="destructive" className="banner bad">
              <AlertDescription>{lic.error}</AlertDescription>
            </Alert>
          )}
          {lic.state === "grace" && (
            <Alert className="banner border-warning/40 bg-warning-soft">
              <AlertDescription className="text-warning">The license has expired. Paid features keep working until {localTime(lic.grace_until!)}; renew it to keep them.</AlertDescription>
            </Alert>
          )}
          <div>
            <h2 className="h2 mb-3 text-sm font-semibold">Features</h2>
            <ul className="feats grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
              {(Object.keys(featureTitles) as Feature[]).map((f) => {
                const on = lic.features.includes(f);
                return (
                  <li key={f} className="flex items-center gap-2 text-sm">
                    <span className={"flex size-5 items-center justify-center rounded-full " + (on ? "bg-success-soft text-success" : "bg-muted text-muted-foreground")}>
                      {on ? <Check className="size-3" /> : <Minus className="size-3" />}
                    </span>
                    <span className={on ? "" : "text-muted-foreground"}>{featureTitles[f]}</span>
                    <span className="sr-only">{on ? "on" : "off"}</span>
                  </li>
                );
              })}
            </ul>
          </div>
          <Note>
            Free without a license: reviews for one GitHub, GitLab and Bitbucket host each, the local executor, repository on/off, <code>@bot</code> commands, review history for {me.free_retention_days} days, the status dashboard and admin sign-in by email.
          </Note>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <KeyRound className="size-4" />
            {lic.state === "none" ? "Install a license" : "Replace the license"}
          </CardTitle>
          <CardDescription>Keys are verified offline; the console never calls home.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {lic.managed ? (
            <p className="text-sm text-muted-foreground">
              {lic.source === "env" ? (
                <>
                  This license comes from <code>CODEREVIEW_CONSOLE_LICENSE</code>, which overrides the one managed here. Remove the variable and restart to manage the license here.
                </>
              ) : (
                <>
                  This license comes from <code>license.file</code> because the console has no database. With <code>database.url</code> set, the key is copied into the database and managed here.
                </>
              )}
            </p>
          ) : (
            <>
              <Field label="License key">
                <Textarea
                  className="key min-h-28 font-mono text-xs"
                  placeholder="DIV1.… or the -----BEGIN DIVMORA LICENSE KEY----- block"
                  autoComplete="off"
                  spellCheck={false}
                  value={key}
                  onChange={(e) => setKey(e.target.value)}
                />
              </Field>
              <div className="flex flex-wrap gap-2">
                <Button disabled={!key.trim()} onClick={() => install(key.trim())}>
                  Install
                </Button>
                {lic.source === "database" && (
                  <Button variant="ghost" className="text-destructive hover:text-destructive" onClick={remove}>
                    Remove license
                  </Button>
                )}
              </div>
              <Note>A pasted key is stored in the database and applies at once, except the executor, which is chosen when the console starts.</Note>
            </>
          )}
        </CardContent>
      </Card>
      <Note>
        Every use of a paid feature needs a license from Divmora, in development, CI and production alike. For a license or a trial, contact{" "}
        <a className="text-primary underline-offset-4 hover:underline" href="mailto:licensing@divmora.com">
          licensing@divmora.com
        </a>
        .
      </Note>
    </section>
  );
}
