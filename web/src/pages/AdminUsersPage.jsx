import { t } from "../i18n";
import { useEffect, useState } from "react";
import { createUser, deleteUser, fetchUsers, updateUser } from "../api/auth";
import "./AdminUsersPage.css";

const emptyForm = { login: "", password: "", displayName: "", role: "user", telegramId: "" };

const ReaderEmailCell = ({ user, onSave }) => {
  const [value, setValue] = useState(user.readerEmail ?? "");

  useEffect(() => {
    setValue(user.readerEmail ?? "");
  }, [user.readerEmail]);

  const commit = () => {
    const next = value.trim();
    if (next === (user.readerEmail ?? "")) return;
    onSave(user, next).catch(() => setValue(user.readerEmail ?? ""));
  };

  return (
    <input
      type="email"
      className="users-table__email"
      aria-label={t("users.readerEmail")}
      placeholder={t("users.readerEmailPh")}
      value={value}
      autoComplete="off"
      spellCheck={false}
      onChange={(e) => setValue(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          e.currentTarget.blur();
        }
      }}
    />
  );
};

const TelegramCell = ({ user, onSave }) => {
  const [value, setValue] = useState(user.telegramId ? String(user.telegramId) : "");

  useEffect(() => {
    setValue(user.telegramId ? String(user.telegramId) : "");
  }, [user.telegramId]);

  const commit = () => {
    const next = value.trim();
    if (next === (user.telegramId ? String(user.telegramId) : "")) return;
    if (next !== "" && !/^\d{1,20}$/.test(next)) {
      alert(t("users.telegramInvalid"));
      setValue(user.telegramId ? String(user.telegramId) : "");
      return;
    }
    onSave(user, next === "" ? 0 : Number(next)).catch(() =>
      setValue(user.telegramId ? String(user.telegramId) : "")
    );
  };

  return (
    <input
      type="text"
      inputMode="numeric"
      className="users-table__email users-table__telegram"
      aria-label={t("users.telegramId")}
      placeholder={t("users.telegramIdPh")}
      value={value}
      autoComplete="off"
      spellCheck={false}
      onChange={(e) => setValue(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          e.currentTarget.blur();
        }
      }}
    />
  );
};

const AdminUsersPage = ({ me, embedded = false }) => {
  const [users, setUsers] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [form, setForm] = useState(emptyForm);
  const [busy, setBusy] = useState(false);

  const reload = () => {
    fetchUsers()
      .then((res) => setUsers(res.users ?? []))
      .catch((err) => setError(err))
      .finally(() => setLoading(false));
  };

  useEffect(reload, []);

  const showError = (err) => {
    if (err.status === 409) {
      const message = String(err.message);
      if (message.includes("telegram")) alert(t("users.telegramTaken"));
      else if (message.includes("admin")) alert(t("users.lastAdmin"));
      else alert(t("users.loginTaken"));
    } else if (err.status === 400) {
      alert(
        String(err.message).includes("telegram")
          ? t("users.telegramInvalid")
          : t("users.emailInvalid")
      );
    } else {
      alert(t("users.opFail"));
    }
  };

  const submit = (e) => {
    e.preventDefault();
    if (!form.login || !form.password || busy) return;
    const payload = { ...form };
    if (payload.telegramId === "") {
      delete payload.telegramId;
    } else {
      if (!/^\d{1,20}$/.test(String(payload.telegramId))) {
        alert(t("users.telegramInvalid"));
        return;
      }
      payload.telegramId = Number(payload.telegramId);
    }
    setBusy(true);
    createUser(payload)
      .then(() => {
        setForm(emptyForm);
        reload();
      })
      .catch(showError)
      .finally(() => setBusy(false));
  };

  const toggleRole = (u) =>
    updateUser(u.id, { role: u.role === "admin" ? "user" : "admin" }).then(reload).catch(showError);

  const toggleDisabled = (u) =>
    updateUser(u.id, { disabled: !u.disabled }).then(reload).catch(showError);

  const saveReaderEmail = (u, readerEmail) =>
    updateUser(u.id, { readerEmail })
      .then(reload)
      .catch((err) => {
        showError(err);
        throw err;
      });

  const saveTelegram = (u, telegramId) =>
    updateUser(u.id, { telegramId })
      .then(reload)
      .catch((err) => {
        showError(err);
        throw err;
      });

  const resetPassword = (u) => {
    const password = prompt(t("users.passwordPrompt", { login: u.login }));
    if (!password) return;
    updateUser(u.id, { password }).then(() => alert(t("users.passwordDone"))).catch(showError);
  };

  const rename = (u) => {
    const name = prompt(t("users.namePrompt", { login: u.login }), u.displayName || "");
    if (name === null) return;
    updateUser(u.id, { displayName: name.trim() })
      .then(() => {
        // The top bar keeps its own copy of the current user.
        if (u.id === me?.id) window.location.reload();
        else reload();
      })
      .catch(showError);
  };

  const remove = (u) => {
    if (!confirm(t("users.deleteConfirm", { login: u.login }))) return;
    deleteUser(u.id).then(reload).catch(showError);
  };

  return (
    <div className={embedded ? "users-page users-page--embedded" : "container users-page"}>
      {embedded ? <h2 className="users-page__title">{t("users.title")}</h2> : (
        <header className="users-page__header">
          <h1>{t("users.title")}</h1>
          <p className="users-page__hint">
            {t("users.hint")}
          </p>
        </header>
      )}
      {embedded && (
        <p className="users-page__hint">
          {t("users.hint")}
        </p>
      )}

      {loading && <div>{t("loading")}</div>}
      {error && !loading && <div className="users-page__error">{t("users.loadFail")}</div>}

      {!loading && !error && (
        <div className="users-table-wrap">
        <table className="users-table">
          <thead>
            <tr>
              <th>{t("users.login")}</th>
              <th>{t("users.name")}</th>
              <th>{t("users.role")}</th>
              <th>{t("users.status")}</th>
              <th>{t("users.telegramId")}</th>
              <th>{t("users.readerEmail")}</th>
              <th aria-label="actions" />
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id} className={u.disabled ? "is-disabled" : ""}>
                <td>{u.login}</td>
                <td>{u.displayName || "—"}</td>
                <td>
                  <span className={`tag ${u.role === "admin" ? "users-table__admin" : ""}`}>
                    {u.role === "admin" ? t("users.admin") : t("users.reader")}
                  </span>
                </td>
                <td>{u.disabled ? t("users.disabled") : t("users.active")}</td>
                <td>
                  <TelegramCell user={u} onSave={saveTelegram} />
                </td>
                <td>
                  <ReaderEmailCell user={u} onSave={saveReaderEmail} />
                </td>
                <td className="users-table__actions-cell">
                  <div className="users-table__actions">
                    <button type="button" className="btn btn-link" onClick={() => rename(u)}>
                      {t("users.rename")}
                    </button>
                    <button type="button" className="btn btn-link" onClick={() => resetPassword(u)}>
                      {t("users.password")}
                    </button>
                    <button type="button" className="btn btn-link" onClick={() => toggleRole(u)}>
                      {u.role === "admin" ? t("users.makeReader") : t("users.makeAdmin")}
                    </button>
                    <button type="button" className="btn btn-link" onClick={() => toggleDisabled(u)}>
                      {u.disabled ? t("users.enable") : t("users.disable")}
                    </button>
                    <button type="button" className="btn btn-link users-table__danger" onClick={() => remove(u)}>
                      {t("users.delete")}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      )}

      <form className="users-form" onSubmit={submit}>
        <h2>{t("users.new")}</h2>
        <div className="users-form__row">
          <input
            type="text"
            placeholder={t("users.login")}
            autoComplete="off"
            value={form.login}
            onChange={(e) => setForm({ ...form, login: e.target.value })}
          />
          <input
            type="password"
            placeholder={t("users.password")}
            autoComplete="new-password"
            value={form.password}
            onChange={(e) => setForm({ ...form, password: e.target.value })}
          />
          <input
            type="text"
            placeholder={t("users.namePh")}
            value={form.displayName}
            onChange={(e) => setForm({ ...form, displayName: e.target.value })}
          />
          <input
            type="text"
            inputMode="numeric"
            placeholder={t("users.telegramIdOptional")}
            value={form.telegramId}
            onChange={(e) => setForm({ ...form, telegramId: e.target.value })}
          />
          <select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
            <option value="user">{t("users.reader")}</option>
            <option value="admin">{t("users.admin")}</option>
          </select>
          <button type="submit" className="btn btn-primary" disabled={busy || !form.login || !form.password}>
            {t("users.create")}
          </button>
        </div>
      </form>
    </div>
  );
};

export default AdminUsersPage;
