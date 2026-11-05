import { currentLang } from "../i18n";

// Formats an external rating as "4,26/5" (or "4.26/5" in English), keeping
// at most two fractional digits and no trailing zeros.
export const formatRating = (value, max = 5, locale) => {
  const n = Number(value);
  const scale = Number(max) || 5;
  if (!(n > 0)) return null;
  const language = locale || (currentLang() === "ru" ? "ru-RU" : "en-US");
  const text = new Intl.NumberFormat(language, {
    minimumFractionDigits: 0,
    maximumFractionDigits: 2,
  }).format(n);
  return `${text}/${scale}`;
};
