import { getLang } from "../i18n";
import api from "./api";

const postForm = async (path, formData) => {
  const res = await fetch(api.buildUrl(path), {
    method: "POST",
    credentials: "include",
    headers: { "X-Polka-Lang": getLang() },
    body: formData,
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    const err = new Error(text || `Request failed: ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return res.json();
};

const post = async (path) => {
  const res = await fetch(api.buildUrl(path), { method: "POST", credentials: "include", headers: { "X-Polka-Lang": getLang() } });
  if (!res.ok) {
    const err = new Error(`Request failed: ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return res.json();
};

const postJson = async (path, body) => {
  const res = await fetch(api.buildUrl(path), {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json", "X-Polka-Lang": getLang() },
    body: JSON.stringify(body ?? {}),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    const err = new Error(text || `Request failed: ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return res.json();
};

export const uploadBooks = (files, { force = false } = {}) => {
  const fd = new FormData();
  for (const f of files) fd.append("files", f);
  if (force) fd.append("force", "1");
  return postForm("admin/books/upload", fd);
};

export const deleteBook = (id) => post(`admin/books/${id}/delete`);
export const restoreBook = (id) => post(`admin/books/${id}/restore`);

export const updateBookMetadata = (id, { title, authors }) =>
  postJson(`admin/books/${id}/metadata`, { title, authors });

export const updateBookEPUB = (id, formData) =>
  postForm(`admin/books/${id}/epub`, formData);

export const forgetBookEnrichment = (id) =>
  post(`admin/books/${id}/forget-enrichment`);

export const refreshBookEnrichment = (id) =>
  post(`api/v1/books/${id}/refresh-enrichment`);

export const checkSources = () => post("admin/sources/check");

export const startEPUBMaintenance = (mode, { deep = false } = {}) => {
  const params = new URLSearchParams({ mode });
  if (deep) params.set("deep", "1");
  return post(`admin/maintenance/epub?${params.toString()}`);
};

export const epubMaintenanceStatus = () => api.getJson("admin/maintenance/epub/status");
export const stopEPUBMaintenance = () => post("admin/maintenance/epub/stop");

export const importInpx = ({ file, path, replace } = {}) => {
  const fd = new FormData();
  if (file) fd.append("file", file);
  if (path) fd.append("path", path);
  if (replace) fd.append("replace", "1");
  return postForm("admin/import/inpx", fd);
};

export const importStatus = () => api.getJson("admin/import/status");
export const fetchConversions = () => api.getJson("admin/conversions");

export const exportUrl = (ids) => api.buildUrl(`Images/export?ids=${ids.join(",")}`);

export const fetchSettings = () => api.getJson("admin/settings");

export const syncGenres = (mode = "all") =>
  post(`admin/genres/sync?mode=${encodeURIComponent(mode)}`);

export const genreSyncStatus = () => api.getJson("admin/genres/status");

export const syncRatings = (mode = "all") =>
  post(`admin/ratings/sync?mode=${encodeURIComponent(mode)}`);
export const ratingSyncStatus = () => api.getJson("admin/ratings/status");

export const testSmtp = () => post("admin/smtp/test");

export const saveSettings = async (settings) => {
  const res = await fetch(api.buildUrl("admin/settings"), {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(settings),
  });
  if (!res.ok) throw new Error(`Request failed: ${res.status}`);
  return res.json();
};
