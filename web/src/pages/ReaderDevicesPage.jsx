import { t } from "../i18n";
import { useEffect, useState } from "react";
import { fetchKosync, setKosyncPassword } from "../api/kosync";
import "./ReaderDevicesPage.css";

const formatSync = (ts) =>
  Number(ts) > 0 ? new Date(Number(ts) * 1000).toLocaleString() : t("readerDevices.never");

const ReaderDevicesPage = () => {
  const [data, setData] = useState(null);
  const [loadError, setLoadError] = useState(false);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState(null);
  const serverUrl = window.location.origin + "/";

  const load = () => {
    setLoadError(false);
    fetchKosync()
      .then(setData)
      .catch(() => setLoadError(true));
  };

  useEffect(load, []);

  const save = () => {
    if (password.length < 8) {
      setMsg({ ok: false, text: t("kosync.tooShort") });
      return;
    }
    setBusy(true);
    setKosyncPassword(password)
      .then(() => {
        setPassword("");
        setMsg({ ok: true, text: t("kosync.saved") });
        load();
      })
      .catch(() => setMsg({ ok: false, text: t("kosync.fail") }))
      .finally(() => setBusy(false));
  };

  const disable = () => {
    if (!confirm(t("readerDevices.disableConfirm"))) return;
    setBusy(true);
    setKosyncPassword("")
      .then(() => {
        setMsg({ ok: true, text: t("kosync.disabled") });
        load();
      })
      .catch(() => setMsg({ ok: false, text: t("kosync.fail") }))
      .finally(() => setBusy(false));
  };

  const enabled = Boolean(data?.enabled);
  const devices = data?.devices ?? [];

  return (
    <div className="container reader-devices">
      <header className="reader-devices__header">
        <h1>{t("readerDevices.title")}</h1>
        <p className="reader-devices__hint">{t("readerDevices.hint")}</p>
      </header>

      {loadError && <div className="reader-devices__error">{t("readerDevices.loadFail")}</div>}

      <section className="reader-devices__card">
        <div className="reader-devices__row">
          <span className="reader-devices__label">{t("readerDevices.server")}</span>
          <code>{serverUrl}</code>
        </div>
        <div className="reader-devices__row">
          <span className="reader-devices__label">{t("readerDevices.login")}</span>
          <span>{data?.login || "…"}</span>
        </div>
        <div className="reader-devices__row">
          <span className="reader-devices__label">{t("readerDevices.status")}</span>
          <span className={enabled ? "reader-devices__on" : "reader-devices__off"}>
            {data === null ? (loadError ? "—" : t("loading")) : enabled ? t("kosync.on") : t("kosync.off")}
          </span>
        </div>
        <div className="reader-devices__row">
          <span className="reader-devices__label">{t("readerDevices.lastSync")}</span>
          <span>{formatSync(data?.lastSync)}</span>
        </div>
      </section>

      <section className="reader-devices__section">
        <h2>{t("readerDevices.setup")}</h2>
        <ol className="reader-devices__steps">
          <li>
            {t("kosync.step.server")} <code>{serverUrl}</code>
          </li>
          <li>
            {t("kosync.step.login")} <code>{data?.login || "…"}</code>
          </li>
          <li>{t("kosync.step.password")}</li>
        </ol>
        <div className="reader-devices__form">
          <input
            type="password"
            autoComplete="new-password"
            placeholder={enabled ? t("kosync.newPassword") : t("kosync.password")}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && save()}
            disabled={busy}
          />
          <button type="button" className="btn btn-primary" onClick={save} disabled={busy}>
            {t("kosync.save")}
          </button>
          {enabled && (
            <button type="button" className="btn btn-ghost" onClick={disable} disabled={busy}>
              {t("kosync.disable")}
            </button>
          )}
        </div>
        {msg && <div className={msg.ok ? "reader-devices__ok" : "reader-devices__error"}>{msg.text}</div>}
      </section>

      <section className="reader-devices__section">
        <div className="reader-devices__section-head">
          <h2>{t("readerDevices.devices")}</h2>
          <button type="button" className="btn btn-link" onClick={load} disabled={busy}>
            {t("readerDevices.refresh")}
          </button>
        </div>
        {loadError ? (
          <p className="reader-devices__error">{t("readerDevices.loadFail")}</p>
        ) : devices.length === 0 ? (
          <p className="reader-devices__hint">{t("readerDevices.noDevices")}</p>
        ) : (
          <ul className="reader-devices__list">
            {devices.map((device, i) => (
              <li key={`${device.device}-${device.deviceId}-${i}`}>
                <div className="reader-devices__device">
                  <span className="reader-devices__device-name">
                    {device.device || t("readerDevices.unnamed")}
                  </span>
                  {device.deviceId && (
                    <span className="reader-devices__device-id">{device.deviceId}</span>
                  )}
                </div>
                <div className="reader-devices__device-meta">
                  <span>{t("readerDevices.documents", { n: Number(device.documents).toLocaleString() })}</span>
                  <span>{formatSync(device.lastSync)}</span>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
};

export default ReaderDevicesPage;
