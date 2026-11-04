// API client for the SPA. In production the frontend is embedded in the Go
// binary and served from the same origin, so baseUrl stays empty.
//
//   .env.local   →   VITE_API_URL=http://192.168.3.31:12791/
//
// Set VITE_API_URL only when running `npm run dev` against a separate server.

const baseUrl = import.meta.env.VITE_API_URL ?? "";

const buildUrl = (path) => {
  if (!path) return baseUrl || "/";
  if (/^https?:/i.test(path)) return path;
  if (baseUrl.endsWith("/") && path.startsWith("/")) return baseUrl + path.slice(1);
  if (!baseUrl.endsWith("/") && !path.startsWith("/")) return `${baseUrl}/${path}`;
  return baseUrl + path;
};

import { getLang } from "../i18n";

const request = async (path, init = {}) => {
  const res = await fetch(buildUrl(path), {
    credentials: "include",
    headers: { "X-Polka-Lang": getLang(), ...(init.headers ?? {}) },
    ...init,
  });
  if (!res.ok) {
    const err = new Error(`Request failed: ${res.status} ${path}`);
    err.status = res.status;
    throw err;
  }
  return res;
};

export const api = {
  baseUrl,
  buildUrl,

  async getJson(path) {
    const res = await request(path);
    return res.json();
  },

  async getBlob(path) {
    const res = await request(path);
    return res.blob();
  },

  async getText(path) {
    const res = await request(path);
    return res.text();
  },

  coverUrl(bookId, version) {
    const url = buildUrl(`Images/covers/${bookId}`);
    return version ? `${url}?v=${encodeURIComponent(version)}` : url;
  },

  fb2Url(bookId) {
    return buildUrl(`Images/fb2/${bookId}`);
  },

  zipUrl(bookId) {
    return buildUrl(`Images/zip/${bookId}`);
  },

  fb2CompactUrl(bookId) {
    return buildUrl(`Images/fb2compact/${bookId}`);
  },
};

export default api;
