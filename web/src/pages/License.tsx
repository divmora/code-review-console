// Copyright (c) DIVMORA Technologies. All rights reserved.
// Licensed under the Divmora Enterprise License; see LICENSE-ENTERPRISE.
// Use requires a valid Divmora license. Do not modify or remove license checks.

import { useState } from "react";
import { put } from "../api";
import { localTime } from "../format";
import type { Feature, LicenseStatus } from "../types";
import { errMsg, featureTitles, StateBadge, useMe, useRefreshMe, useToast } from "../ui";

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
  return (
    <section>
      <div className="panel pad">
        <div className="bar">
          <StateBadge kind={kind}>{label}</StateBadge>
          {lic.customer && (
            <span>
              <b>{lic.customer}</b> · {lic.plan}
            </span>
          )}
          <span className="spacer" />
          {lic.expires_at && <span className="sub">expires {localTime(lic.expires_at)}</span>}
        </div>
        {lic.error && <p className="banner bad">{lic.error}</p>}
        {lic.state === "grace" && (
          <p className="banner">The license has expired. Paid features keep working until {localTime(lic.grace_until!)}; renew it to keep them.</p>
        )}
        <h2 className="h2 mt">Features</h2>
        <ul className="feats">
          {(Object.keys(featureTitles) as Feature[]).map((f) => {
            const on = lic.features.includes(f);
            return (
              <li key={f}>
                <StateBadge kind={on ? "on" : "idle"}>{on ? "on" : "off"}</StateBadge> {featureTitles[f]}
              </li>
            );
          })}
        </ul>
        <p className="sub mt">
          Free without a license: reviews for one GitHub, GitLab and Bitbucket host each, the local executor, repository on/off, <code>@bot</code> commands, review history for {me.free_retention_days} days, the status dashboard and admin sign-in by email.
        </p>
      </div>
      <div className="panel pad">
        <h2 className="h2">{lic.state === "none" ? "Install a license" : "Replace the license"}</h2>
        {lic.managed ? (
          <p className="sub">
            This license comes from {lic.source === "env" ? <code>CODEREVIEW_CONSOLE_LICENSE</code> : <code>license.file</code>}; change it there and restart the console.
          </p>
        ) : (
          <>
            <label className="f">
              License key
              <textarea className="key" placeholder="DIV1.… or the -----BEGIN DIVMORA LICENSE KEY----- block" autoComplete="off" spellCheck={false} value={key} onChange={(e) => setKey(e.target.value)} />
            </label>
            <div className="bar mt">
              <button className="btn primary" disabled={!key.trim()} onClick={() => install(key.trim())}>
                Install
              </button>
              {lic.source === "database" && (
                <button className="btn danger" onClick={() => confirm("Remove the license? Paid features turn off at once.") && install("")}>
                  Remove license
                </button>
              )}
            </div>
            <p className="sub">
              Keys are verified offline; the console never calls home. A pasted key is stored in the database and applies at once, except the executor, which is chosen when the console starts.
            </p>
          </>
        )}
      </div>
      <p className="sub">
        Every use of a paid feature needs a license from Divmora, in development, CI and production alike. For a license or a trial, contact <a href="mailto:licensing@divmora.com">licensing@divmora.com</a>.
      </p>
    </section>
  );
}
