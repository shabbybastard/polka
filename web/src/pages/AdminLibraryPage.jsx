import { t } from "../i18n";
import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import AdminUsersPage from "./AdminUsersPage";
import { Link } from "react-router-dom";
import {
  checkSources,
  epubMaintenanceStatus,
  fetchConversions,
  fetchSettings,
  genreSyncStatus,
  importInpx,
  importStatus,
  ratingSyncStatus,
  saveSettings,
  startEPUBMaintenance,
  stopEPUBMaintenance,
  syncGenres,
  syncRatings,
  testSmtp,
  uploadBooks,
} from "../api/manage";
import { deleteCollection, fetchCollections, importCollection, rematchCollection, syncCollectionSource } from "../api/collections";
import "./AdminLibraryPage.css";

const ENRICH_SOURCES = () => [
  { id: "livelib", label: "LiveLib", hint: t("admin.livelib.hint") },
  { id: "fantlab", label: t("source.fantlab"), hint: t("admin.fantlab.ratingHint") },
  { id: "google_books", label: "Google Books", hint: t("admin.google.hint") },
  { id: "open_library", label: "Open Library", hint: t("admin.openlibrary.hint") },
];

const TAG_SOURCES = () => [
  { id: "livelib", label: "LiveLib", hint: t("admin.tags.livelib") },
  { id: "open_library", label: "Open Library", hint: t("admin.tags.openlibrary") },
];

const PHASE_LABELS = () => ({
  starting: t("admin.import.starting"),
  clearing: t("admin.import.clearing"),
  loading: t("admin.import.loading"),
  indexing: t("admin.import.indexing"),
});

const SOURCE_STATE_LABELS = () => ({
  ok: t("admin.sources.state.ok"),
  degraded: t("admin.sources.state.degraded"),
  error: t("admin.sources.state.error"),
  missing_key: t("admin.sources.state.missingKey"),
  disabled: t("admin.sources.state.disabled"),
});

const SourceCheckBadge = ({ check }) => {
  if (!check) return null;
  const state = check.state || "error";
  const label = SOURCE_STATE_LABELS()[state] ?? state;
  const details = [];
  if (state === "ok" || state === "degraded") {
    if (check.status) details.push(`HTTP ${check.status}`);
  } else if (check.message) {
    details.push(check.message);
  }
  if (check.latencyMs) details.push(`${check.latencyMs} ${t("admin.sources.ms")}`);
  return (
    <span className={`library-admin__source-check library-admin__source-check--${state}`}>
      {label}
      {details.length > 0 ? ` · ${details.join(" · ")}` : ""}
    </span>
  );
};

// External collection sources (Forbes) are temporarily hidden from the
// UI: the backend and its settings stay intact, so the block below can be
// restored by flipping this flag instead of reverting code.
const SHOW_COLLECTION_SOURCES = false;
const CONVERSION_DISMISSED_KEY = "polka-conversion-dismissed";
const GENRE_STATUS_DISMISSED_KEY = "polka-genre-status-dismissed";
const RATING_STATUS_DISMISSED_KEY = "polka-rating-status-dismissed";

const conversionSignature = (jobs) =>
  jobs?.length ? `${jobs[0].id}:${jobs[0].updatedAt ?? ""}` : "";

const AdminLibraryPage = ({ onConfigChange, me, desktop = false }) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawTab = searchParams.get("tab");
  const legacyPanel = rawTab === "import" || rawTab === "collections" ? rawTab : "";
  const VALID_TABS = desktop
    ? ["books", "sources", "readers", "appearance", "maintenance"]
    : ["books", "sources", "users", "readers", "appearance", "maintenance"];
  const tab = VALID_TABS.includes(rawTab) ? rawTab : "books";
  const selectTab = (id) => setSearchParams(id === "books" ? {} : { tab: id }, { replace: true });
  // Import and collections used to be separate tabs. They now live inside
  // "Books" as collapsible blocks; keep old links working by opening the
  // matching block.
  const [openPanels, setOpenPanels] = useState(() => ({
    import: legacyPanel === "import",
    collections: legacyPanel === "collections",
  }));
  useEffect(() => {
    if (legacyPanel) {
      setOpenPanels({ import: legacyPanel === "import", collections: legacyPanel === "collections" });
    }
  }, [legacyPanel]);
  const togglePanel = (id) => setOpenPanels((prev) => ({ ...prev, [id]: !prev[id] }));
  // --- Book upload ---
  const [uploading, setUploading] = useState(false);
  const [results, setResults] = useState([]);
  const [convert, setConvert] = useState({ fb2ToEpub: false, keepSource: true });
  const [telegram, setTelegram] = useState({ uploadRateLimit: 50 });
  const [conversionJobs, setConversionJobs] = useState([]);
  const [conversionHidden, setConversionHidden] = useState(() => {
    try {
      return Boolean(localStorage.getItem(CONVERSION_DISMISSED_KEY));
    } catch {
      return false;
    }
  });
  const conversionPolling = useRef(null);
  const conversionDone = useRef(new Set());
  const fileInput = useRef(null);
  const pendingFiles = useRef(new Map()); // file name -> File, for re-uploading with force

  const mergeResults = (incoming) =>
    setResults((prev) => [
      ...incoming,
      ...prev.filter((p) => !incoming.some((n) => n.name === p.name)),
    ]);

  const refreshConversions = () => {
    fetchConversions()
      .then((res) => {
        const jobs = res?.jobs ?? [];
        setConversionJobs(jobs);
        let newlyDone = false;
        for (const job of jobs) {
          if (job.status === "done" && !conversionDone.current.has(job.id)) {
            conversionDone.current.add(job.id);
            newlyDone = true;
          }
        }
        if (newlyDone) onConfigChange?.();
        const active = jobs.some((job) => job.status === "queued" || job.status === "processing");
        let dismissed = "";
        try {
          dismissed = localStorage.getItem(CONVERSION_DISMISSED_KEY) ?? "";
        } catch {
          /* ignore */
        }
        if (active) {
          setConversionHidden(false);
          try {
            localStorage.removeItem(CONVERSION_DISMISSED_KEY);
          } catch {
            /* ignore */
          }
        } else if (dismissed && dismissed === conversionSignature(jobs)) {
          setConversionHidden(true);
        } else {
          setConversionHidden(false);
        }
        if (active && !conversionPolling.current) {
          conversionPolling.current = setInterval(refreshConversions, 1500);
        } else if (!active && conversionPolling.current) {
          clearInterval(conversionPolling.current);
          conversionPolling.current = null;
        }
      })
      .catch(() => {});
  };

  useEffect(() => {
    refreshConversions();
    return () => {
      if (conversionPolling.current) {
        clearInterval(conversionPolling.current);
        conversionPolling.current = null;
      }
    };
  }, []);

  const onUpload = (e) => {
    const files = Array.from(e.target.files ?? []);
    if (!files.length) return;
    setUploading(true);
    for (const f of files) pendingFiles.current.set(f.name, f);
    uploadBooks(files)
      .then((res) => {
        mergeResults(res.results ?? []);
        if ((res.results ?? []).some((r) => r.status === "queued")) refreshConversions();
      })
      .catch(() => alert(t("admin.upload.fail")))
      .finally(() => {
        setUploading(false);
        if (fileInput.current) fileInput.current.value = "";
      });
  };

  const forceUpload = (name) => {
    const file = pendingFiles.current.get(name);
    if (!file || uploading) return;
    setUploading(true);
    uploadBooks([file], { force: true })
      .then((res) => {
        mergeResults(res.results ?? []);
        if ((res.results ?? []).some((r) => r.status === "queued")) refreshConversions();
      })
      .catch(() => alert(t("admin.upload.fail")))
      .finally(() => setUploading(false));
  };

  // --- inpx import ---
  const [inpxFile, setInpxFile] = useState(null);
  const [inpxPath, setInpxPath] = useState("");
  const [replace, setReplace] = useState(false);
  const [status, setStatus] = useState(null);
  const [importStatusHidden, setImportStatusHidden] = useState(false);
  const polling = useRef(null);

  const refreshStatus = () =>
    importStatus()
      .then((st) => {
        setStatus(st);
        if (st.running) {
          setImportStatusHidden(false);
          // A page reload during an import must resume polling too, not just
          // the initial startImport call.
          if (!polling.current) polling.current = setInterval(refreshStatus, 1000);
        }
        if (!st.running && polling.current) {
          clearInterval(polling.current);
          polling.current = null;
        }
      })
      .catch(() => {});

  useEffect(() => {
    refreshStatus();
    return () => polling.current && clearInterval(polling.current);
  }, []);

  const startImport = () => {
    if (!inpxFile && !inpxPath.trim()) return;
    if (replace && !confirm(t("admin.import.confirm"))) return;
    setImportStatusHidden(false);
    importInpx({ file: inpxFile, path: inpxPath.trim(), replace })
      .then(() => {
        setInpxFile(null);
        setInpxPath("");
        refreshStatus();
        if (!polling.current) polling.current = setInterval(refreshStatus, 1000);
      })
      .catch((err) => {
        alert(err.status === 409 ? t("admin.import.conflict") : t("admin.import.fail"));
      });
  };

  const importRunning = Boolean(status?.running);

  // --- Sources for external ratings and similar books ---
  const [enrichment, setEnrichment] = useState(null);
  const [tagSources, setTagSources] = useState({ livelib: false, open_library: false });
  const [genreStatus, setGenreStatus] = useState(null);
  const [genreStatusHidden, setGenreStatusHidden] = useState(() => {
    try { return localStorage.getItem(GENRE_STATUS_DISMISSED_KEY) === "1"; } catch { return false; }
  });
  const genrePolling = useRef(null);
  const [ratingStatus, setRatingStatus] = useState(null);
  const [ratingStatusHidden, setRatingStatusHidden] = useState(() => {
    try { return localStorage.getItem(RATING_STATUS_DISMISSED_KEY) === "1"; } catch { return false; }
  });
  const ratingPolling = useRef(null);
  const [similar, setSimilar] = useState(null);
  const [tastediveKey, setTastediveKey] = useState("");
  const [sourceChecks, setSourceChecks] = useState(null);
  const [sourceCheckBusy, setSourceCheckBusy] = useState(false);
  const [sourceCheckedAt, setSourceCheckedAt] = useState("");
  const [googleBooksKey, setGoogleBooksKey] = useState("");
  const [homeHero, setHomeHero] = useState(true);
  const [opdsEnabled, setOpdsEnabled] = useState(true);
  const [opdsCopied, setOpdsCopied] = useState(false);
  const [smtp, setSmtp] = useState(null);
  const [smtpPwd, setSmtpPwd] = useState("");
  const [smtpMsg, setSmtpMsg] = useState(null);
  const [library, setLibrary] = useState(null);
  const [colSources, setColSources] = useState(null);

  const pollGenreStatus = () => {
    genreSyncStatus()
      .then((st) => {
        setGenreStatus(st);
        if (st.running) {
          setGenreStatusHidden(false);
          try { localStorage.removeItem(GENRE_STATUS_DISMISSED_KEY); } catch { /* ignore */ }
          if (!genrePolling.current) {
            genrePolling.current = setInterval(pollGenreStatus, 1000);
          }
        } else if (genrePolling.current) {
          clearInterval(genrePolling.current);
          genrePolling.current = null;
        }
      })
      .catch(() => {});
  };

  useEffect(() => {
    pollGenreStatus();
    return () => {
      if (genrePolling.current) {
        clearInterval(genrePolling.current);
        genrePolling.current = null;
      }
    };
  }, []);

  const pollRatingStatus = () => {
    ratingSyncStatus()
      .then((st) => {
        setRatingStatus(st);
        if (st.running) {
          setRatingStatusHidden(false);
          try { localStorage.removeItem(RATING_STATUS_DISMISSED_KEY); } catch { /* ignore */ }
          if (!ratingPolling.current) {
            ratingPolling.current = setInterval(pollRatingStatus, 1000);
          }
        } else if (ratingPolling.current) {
          clearInterval(ratingPolling.current);
          ratingPolling.current = null;
        }
      })
      .catch(() => {});
  };

  useEffect(() => {
    pollRatingStatus();
    return () => {
      if (ratingPolling.current) {
        clearInterval(ratingPolling.current);
        ratingPolling.current = null;
      }
    };
  }, []);

  const [maintenanceStatus, setMaintenanceStatus] = useState(null);
  const [maintenanceDeep, setMaintenanceDeep] = useState(true);
  const maintenancePolling = useRef(null);

  const pollMaintenanceStatus = () => {
    epubMaintenanceStatus()
      .then((st) => {
        setMaintenanceStatus(st);
        if (st.running) {
          if (!maintenancePolling.current) {
            maintenancePolling.current = setInterval(pollMaintenanceStatus, 1000);
          }
        } else if (maintenancePolling.current) {
          clearInterval(maintenancePolling.current);
          maintenancePolling.current = null;
        }
      })
      .catch(() => {});
  };

  useEffect(() => {
    pollMaintenanceStatus();
    return () => {
      if (maintenancePolling.current) {
        clearInterval(maintenancePolling.current);
        maintenancePolling.current = null;
      }
    };
  }, []);

  const startMaintenance = (mode) => {
    if (maintenanceStatus?.running) return;
    if (mode === "fix" && !confirm(t("admin.maintenance.confirmFix"))) return;
    startEPUBMaintenance(mode, { deep: maintenanceDeep })
      .then(() => pollMaintenanceStatus())
      .catch((err) =>
        alert(err.status === 409 ? t("admin.maintenance.running") : t("admin.maintenance.startFail"))
      );
  };

  const stopMaintenance = () => {
    stopEPUBMaintenance()
      .then(() => pollMaintenanceStatus())
      .catch(() => alert(t("admin.maintenance.startFail")));
  };

  useEffect(() => {
    fetchSettings()
      .then((res) => {
        setEnrichment(res.enrichment ?? null);
        setTagSources({ livelib: false, open_library: false, ...(res.tagSources ?? {}) });
        setColSources(res.collectionSources ?? []);
        setSimilar(res.similar ?? null);
        setTastediveKey(res.tastediveKey ?? "");
        setGoogleBooksKey(res.googleBooksKey ?? "");
        setHomeHero(res.homeHero !== false);
        setOpdsEnabled(res.opdsEnabled !== false);
        setConvert({ fb2ToEpub: false, keepSource: true, ...(res.convert ?? {}) });
        setTelegram({ uploadRateLimit: 50, ...(res.telegram ?? {}) });
        setSmtp(res.smtp ?? { security: "starttls", port: 587 });
        setLibrary(res.library ?? null);
      })
      .catch(() => setEnrichment(null));
  }, []);

  // An empty / missing library root almost always means the mounted path and
  // POLKA_LIBRARY_DIR disagree: the catalog imports, but books "won't open"
  // and covers are missing.
  const libraryEmpty = library && library.configured && (!library.exists || library.entries === 0);

  const toggleSource = (id) => {
    const next = { ...enrichment, [id]: !enrichment[id] };
    setEnrichment(next);
    saveSettings({ enrichment: next }).catch(() => {
      setEnrichment(enrichment); // roll back on error
      alert(t("admin.sources.saveFail"));
    });
  };

  const toggleTagSource = (id) => {
    const next = { ...tagSources, [id]: !tagSources[id] };
    setTagSources(next);
    saveSettings({ tagSources: next }).catch(() => {
      setTagSources(tagSources);
      alert(t("admin.sources.saveFail"));
    });
  };

  const runSourceChecks = () => {
    if (sourceCheckBusy) return;
    setSourceCheckBusy(true);
    checkSources()
      .then((res) => {
        const next = {};
        for (const check of res.sources ?? []) next[check.source] = check;
        setSourceChecks(next);
        setSourceCheckedAt(res.checkedAt ?? "");
      })
      .catch(() => alert(t("admin.sources.checkFail")))
      .finally(() => setSourceCheckBusy(false));
  };

  const startGenreSync = (mode) => {
    setGenreStatusHidden(false);
    try { localStorage.removeItem(GENRE_STATUS_DISMISSED_KEY); } catch { /* ignore */ }
    if (!tagSources.livelib && !tagSources.open_library) {
      alert(t("admin.genres.noSource"));
      return;
    }
    if (!confirm(t(mode === "empty" ? "admin.genres.confirmEmpty" : "admin.genres.confirm"))) return;
    syncGenres(mode)
      .then(() => pollGenreStatus())
      .catch((err) => alert(err.status === 409 ? t("admin.genres.running") : t("admin.genres.fail")));
  };

  const anyRatingSource = Object.values(enrichment ?? {}).some(Boolean);

  const startRatingSync = (mode) => {
    setRatingStatusHidden(false);
    try { localStorage.removeItem(RATING_STATUS_DISMISSED_KEY); } catch { /* ignore */ }
    if (!anyRatingSource) {
      alert(t("admin.ratings.noSource"));
      return;
    }
    if (!confirm(t(mode === "empty" ? "admin.ratings.confirmEmpty" : "admin.ratings.confirm"))) return;
    syncRatings(mode)
      .then(() => pollRatingStatus())
      .catch((err) =>
        alert(err.status === 409 ? t("admin.ratings.running") : t("admin.ratings.fail"))
      );
  };

  const toggleSimilar = (id) => {
    const next = { ...similar, [id]: !similar[id] };
    setSimilar(next);
    saveSettings({ similar: next }).catch(() => {
      setSimilar(similar);
      alert(t("admin.sources.saveFail"));
    });
  };

  const saveTastediveKey = () => {
    saveSettings({ tastediveKey })
      .then((res) => {
        setSimilar(res.similar ?? similar);
        alert(t("admin.tastedive.saved"));
      })
      .catch(() => alert(t("admin.tastedive.saveFail")));
  };

  const saveGoogleBooksKey = () => {
    saveSettings({ googleBooksKey })
      .then((res) => {
        setGoogleBooksKey(res.googleBooksKey ?? "");
        alert(t("admin.google.keySaved"));
      })
      .catch(() => alert(t("admin.google.keySaveFail")));
  };

  const toggleHomeHero = () => {
    const next = !homeHero;
    setHomeHero(next);
    saveSettings({ homeHero: next })
      // Root owns the config used by HomePage; refresh it so the banner
      // appears/disappears without a full page reload.
      .then(() => onConfigChange?.())
      .catch(() => {
        setHomeHero(!next);
        alert(t("admin.sources.saveFail"));
      });
  };

  const toggleConvert = (key) => {
    const next = { ...convert, [key]: !convert[key] };
    setConvert(next);
    saveSettings({ convert: next }).catch(() => {
      setConvert(convert);
      alert(t("admin.sources.saveFail"));
    });
  };

  const saveTelegramLimit = () => {
    const n = Number(telegram.uploadRateLimit);
    if (!Number.isInteger(n) || n < 1 || n > 10000) {
      alert(t("admin.telegram.limitInvalid"));
      return;
    }
    saveSettings({ telegram: { uploadRateLimit: n } })
      .then((res) => setTelegram({ uploadRateLimit: 50, ...(res.telegram ?? {}) }))
      .catch(() => alert(t("admin.sources.saveFail")));
  };

  const opdsUrl = `${window.location.origin}/opds`;

  const toggleOpds = () => {
    const next = !opdsEnabled;
    setOpdsEnabled(next);
    saveSettings({ opdsEnabled: next }).catch(() => setOpdsEnabled(!next));
  };

  const copyOpds = () => {
    navigator.clipboard?.writeText(opdsUrl).then(() => {
      setOpdsCopied(true);
      setTimeout(() => setOpdsCopied(false), 1500);
    });
  };

  const smtpField = (key, value) => setSmtp((prev) => ({ ...prev, [key]: value }));

  const saveSmtp = () => {
    const payload = { ...smtp, port: Number(smtp.port) || 0 };
    if (smtpPwd) payload.password = smtpPwd;
    setSmtpMsg(null);
    saveSettings({ smtp: payload })
      .then((res) => {
        setSmtp(res.smtp ?? smtp);
        setSmtpPwd("");
        setSmtpMsg({ ok: true, text: t("admin.smtp.saved") });
      })
      .catch(() => setSmtpMsg({ ok: false, text: t("admin.smtp.saveFail") }));
  };

  const checkSmtp = () => {
    setSmtpMsg({ ok: true, text: t("admin.smtp.checking") });
    testSmtp()
      .then((res) =>
        setSmtpMsg(res.ok ? { ok: true, text: t("admin.smtp.ok") } : { ok: false, text: res.error || t("admin.smtp.checkFail") })
      )
      .catch(() => setSmtpMsg({ ok: false, text: t("admin.smtp.checkFail") }));
  };

  // --- подборки ---
  const [collections, setCollections] = useState([]);
  const [colBusy, setColBusy] = useState(false);
  const [colMsg, setColMsg] = useState(null);
  const colInputRef = useRef(null);
  const reloadCollections = () =>
    fetchCollections()
      .then((res) => setCollections(res.collections ?? []))
      .catch(() => setCollections([]));
  useEffect(() => {
    reloadCollections();
  }, []);
  const onCollectionFiles = async (files) => {
    if (!files?.length) return;
    setColBusy(true);
    setColMsg(null);
    try {
      for (const f of files) {
        const c = await importCollection(f);
        setColMsg({ ok: true, text: t("admin.collections.imported", { title: c.title, matched: c.matched, total: c.total }) });
      }
      await reloadCollections();
    } catch (err) {
      setColMsg({ ok: false, text: t("admin.collections.fail", { err: err.message || "" }) });
    } finally {
      setColBusy(false);
      if (colInputRef.current) colInputRef.current.value = "";
    }
  };
  const rematch = (slug) => {
    setColBusy(true);
    rematchCollection(slug)
      .then((c) => {
        setColMsg({ ok: true, text: t("admin.collections.imported", { title: c.title, matched: c.matched, total: c.total }) });
        return reloadCollections();
      })
      .catch(() => setColMsg({ ok: false, text: t("admin.collections.fail", { err: "" }) }))
      .finally(() => setColBusy(false));
  };
  const SOURCE_LABELS = { forbes: { label: "Forbes.ru", hint: t("admin.collections.forbes.hint") } };
  const toggleColSource = (id, on) => {
    saveSettings({ collectionSources: { [id]: on } })
      .then((res) => {
        setColSources(res.collectionSources ?? []);
        if (on) setColMsg({ ok: true, text: t("admin.collections.source.started") });
      })
      .catch(() => alert(t("admin.sources.saveFail")));
  };
  const syncColSource = (id) => {
    syncCollectionSource(id)
      .then(() => setColMsg({ ok: true, text: t("admin.collections.source.started") }))
      .catch(() => setColMsg({ ok: false, text: t("admin.collections.fail", { err: "" }) }));
    setTimeout(() => fetchSettings().then((res) => setColSources(res.collectionSources ?? [])).then(reloadCollections), 8000);
  };
  const removeCollection = (c) => {
    if (!confirm(t("admin.collections.confirmDelete", { title: c.title }))) return;
    deleteCollection(c.slug)
      .then(reloadCollections)
      .catch(() => setColMsg({ ok: false, text: t("admin.collections.fail", { err: "" }) }));
  };

  const genrePercent =
    genreStatus && Number(genreStatus.total) > 0
      ? Math.min(100, Math.round((Number(genreStatus.processed) / Number(genreStatus.total)) * 100))
      : 0;

  const ratingPercent =
    ratingStatus && Number(ratingStatus.total) > 0
      ? Math.min(100, Math.round((Number(ratingStatus.processed) / Number(ratingStatus.total)) * 100))
      : 0;

  const maintenancePercent =
    maintenanceStatus && Number(maintenanceStatus.total) > 0
      ? Math.min(100, Math.round((Number(maintenanceStatus.processed) / Number(maintenanceStatus.total)) * 100))
      : 0;

  const conversionQueued = conversionJobs.filter((job) => job.status === "queued").length;
  const conversionProcessing = conversionJobs.filter((job) => job.status === "processing").length;
  const conversionActive = conversionQueued + conversionProcessing > 0;
  const conversionFinished = conversionJobs.length - conversionQueued - conversionProcessing;
  const conversionPercent =
    conversionJobs.length > 0
      ? Math.min(100, Math.round((conversionFinished / conversionJobs.length) * 100))
      : 0;

  const dismissConversions = () => {
    setConversionHidden(true);
    const signature = conversionSignature(conversionJobs);
    if (!signature) return;
    try {
      localStorage.setItem(CONVERSION_DISMISSED_KEY, signature);
    } catch {
      /* ignore */
    }
  };

  return (
    <div className="container library-admin">
      <header className="library-admin__header">
        <h1>{t("admin.title")}</h1>
      </header>

      {libraryEmpty && (
        <div className="library-admin__banner library-admin__banner--warning">
          {t("admin.library.empty", { dir: library.dir })}
        </div>
      )}

      <nav className="library-admin__tabs" aria-label={t("admin.tabs")}>
        {[
          { id: "books", label: t("admin.tab.books") },
          { id: "sources", label: t("admin.tab.sources") },
          ...(desktop ? [] : [{ id: "users", label: t("admin.tab.users") }]),
          { id: "readers", label: t("admin.tab.readers") },
          { id: "appearance", label: t("admin.tab.appearance") },
          { id: "maintenance", label: t("admin.tab.maintenance") },
        ].map((tb) => (
          <button
            key={tb.id}
            type="button"
            className={`library-admin__tab ${tab === tb.id ? "is-active" : ""}`}
            onClick={() => selectTab(tb.id)}
          >
            {tb.label}
          </button>
        ))}
      </nav>

      {tab === "users" && (
        <AdminUsersPage me={me} embedded />
      )}

      {tab === "books" && (
      <section className="library-admin__section">
        <h2>{t("admin.upload")}</h2>
        <p className="library-admin__hint">
          {t("admin.upload.hint")}
        </p>
        <div className="library-admin__convert">
          <label className="library-admin__source">
            <input
              type="checkbox"
              checked={Boolean(convert.fb2ToEpub)}
              onChange={() => toggleConvert("fb2ToEpub")}
            />
            <span className="library-admin__source-name">{t("admin.convert.fb2ToEpub")}</span>
            <span className="library-admin__source-hint">{t("admin.convert.fb2ToEpubHint")}</span>
          </label>
          <label className="library-admin__source">
            <input
              type="checkbox"
              checked={Boolean(convert.keepSource)}
              onChange={() => toggleConvert("keepSource")}
            />
            <span className="library-admin__source-name">{t("admin.convert.keepSource")}</span>
            <span className="library-admin__source-hint">{t("admin.convert.keepSourceHint")}</span>
          </label>
        </div>
        <div className="library-admin__telegram-limit">
          <label>
            <span className="library-admin__source-name">{t("admin.telegram.uploadLimit")}</span>
            <input
              type="number"
              min="1"
              max="10000"
              value={telegram.uploadRateLimit}
              onChange={(e) => setTelegram({ ...telegram, uploadRateLimit: e.target.value })}
              onBlur={saveTelegramLimit}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  e.currentTarget.blur();
                }
              }}
            />
          </label>
          <span className="library-admin__source-hint">{t("admin.telegram.uploadLimitHint")}</span>
        </div>
        <label className={`library-admin__dropzone ${uploading ? "is-busy" : ""}`}>
          <input
            ref={fileInput}
            type="file"
            multiple
            accept=".fb2,.epub,.pdf,.txt,.mobi,.azw3"
            onChange={onUpload}
            disabled={uploading}
          />
          {uploading ? t("admin.upload.busy") : t("admin.upload.drop")}
        </label>

        {results.length > 0 && (
          <ul className="library-admin__results">
            {results.map((r, i) => (
              <li key={`${r.name}-${i}`} className={r.error ? "is-error" : ""}>
                {r.error ? (
                  <>
                    <span className="library-admin__file">{r.name}</span>
                    <span className="library-admin__error">
                      {r.error}
                      {r.duplicate && (
                        <>
                          {": "}
                          <Link to={`/book/${r.duplicate.bookId}`}>«{r.duplicate.title}»</Link>
                        </>
                      )}
                    </span>
                  </>
                ) : r.needConfirm ? (
                  <>
                    <span className="library-admin__file">{r.name}</span>
                    <span className="library-admin__warn">
                      {t("admin.upload.similar")}{" "}
                      <Link to={`/book/${r.duplicate.bookId}`}>
                        «{r.duplicate.title}»{r.duplicate.authors ? ` — ${r.duplicate.authors}` : ""}
                      </Link>
                    </span>
                    <button
                      type="button"
                      className="btn btn-link"
                      disabled={uploading}
                      onClick={() => forceUpload(r.name)}
                    >
                      {t("admin.upload.force")}
                    </button>
                  </>
                ) : r.status === "queued" ? (
                  <>
                    <span className="library-admin__file">{r.name}</span>
                    <span className="library-admin__warn">{t("admin.convert.queued")}</span>
                  </>
                ) : (
                  <>
                    <Link to={`/book/${r.bookId}`} className="library-admin__file">
                      {r.title}
                    </Link>
                    {r.authors && <span className="library-admin__authors">{r.authors}</span>}
                    <span className="library-admin__ok">{t("admin.upload.added")}</span>
                  </>
                )}
              </li>
            ))}
          </ul>
        )}

        {!conversionHidden && conversionJobs.length > 0 && (
          <div className="library-admin__genre-status library-admin__conversion-card">
            <div className="library-admin__genre-status-head">
              <span className="library-admin__genre-mode">{t("admin.convert.jobs")}</span>
              <span className="library-admin__genre-progress">
                {conversionActive
                  ? t("admin.convert.queueActive", {
                      queued: conversionQueued,
                      processing: conversionProcessing,
                    })
                  : t("admin.convert.queueDone", {
                      done: conversionFinished,
                      total: conversionJobs.length,
                    })}
              </span>
            </div>
            <div
              className="library-admin__genre-bar"
              role="progressbar"
              aria-valuemin="0"
              aria-valuemax={conversionJobs.length || 0}
              aria-valuenow={conversionFinished}
            >
              <span style={{ width: `${conversionPercent}%` }} />
            </div>
            <ul className="library-admin__results library-admin__conversions">
              {conversionJobs.map((job) => (
                <li key={job.id} className={`is-${job.status}`}>
                  <span className="library-admin__file">{job.name}</span>
                  <span className={`library-admin__conversion-status is-${job.status}`}>
                    {t(`admin.convert.status.${job.status}`)}
                  </span>
                  {job.bookId > 0 && job.status !== "failed" && (
                    <Link to={`/book/${job.bookId}`} className="library-admin__conversion-link">
                      {t("admin.convert.open")}
                    </Link>
                  )}
                  {job.status === "failed" && job.error && (
                    <span className="library-admin__error">{job.error}</span>
                  )}
                </li>
              ))}
            </ul>
            {!conversionActive && (
              <div className="library-admin__status-close">
                <button
                  type="button"
                  className="btn btn-link"
                  onClick={dismissConversions}
                >
                  {t("admin.progress.close")}
                </button>
              </div>
            )}
          </div>
        )}

        <div className="library-admin__spoilers">
          <button
            type="button"
            className={`library-admin__spoiler-toggle ${openPanels.import ? "is-open" : ""}`}
            aria-expanded={openPanels.import}
            onClick={() => togglePanel("import")}
          >
            <span>{t("admin.import")}</span>
            <span className="library-admin__spoiler-mark" aria-hidden="true">
              {openPanels.import ? "−" : "+"}
            </span>
          </button>
          <button
            type="button"
            className={`library-admin__spoiler-toggle ${openPanels.collections ? "is-open" : ""}`}
            aria-expanded={openPanels.collections}
            onClick={() => togglePanel("collections")}
          >
            <span>{t("admin.collections")}</span>
            <span className="library-admin__spoiler-mark" aria-hidden="true">
              {openPanels.collections ? "−" : "+"}
            </span>
          </button>
        </div>
      </section>
      )}

      {tab === "appearance" && (
      <section className="library-admin__section">
        <h2>{t("admin.home")}</h2>
        <p className="library-admin__hint">{t("admin.home.hint")}</p>
        <label className="library-admin__source library-admin__opds-toggle">
          <input type="checkbox" checked={homeHero} onChange={toggleHomeHero} />
          <span className="library-admin__source-name">{t("admin.home.hero")}</span>
        </label>
      </section>
      )}

      {tab === "books" && openPanels.collections && (
      <section className="library-admin__section library-admin__spoiler-panel">
        <h2>{t("admin.collections")}</h2>
        <p className="library-admin__hint">
          {t("admin.collections.hint")}{" "}
          <a href="https://github.com/shabbybastard/polka/tree/main/collections" target="_blank" rel="noreferrer">
            {t("admin.collections.examples")}
          </a>
          {" · "}
          {t("admin.collections.bundled")}
        </p>
        <div className="library-admin__import-row">
          <input
            ref={colInputRef}
            type="file"
            accept=".json,application/json"
            multiple
            disabled={colBusy}
            onChange={(e) => onCollectionFiles(Array.from(e.target.files ?? []))}
          />
          {colBusy && <span className="library-admin__spinner" aria-hidden="true" />}
          {colMsg && (
            <span className={colMsg.ok ? "library-admin__ok" : "library-admin__error"}>{colMsg.text}</span>
          )}
        </div>
        {SHOW_COLLECTION_SOURCES && colSources && colSources.length > 0 && (
          <>
            <h3 className="library-admin__subtitle">{t("admin.collections.sources")}</h3>
            <ul className="library-admin__sources">
              {colSources.map((src) => {
                const meta = SOURCE_LABELS[src.id] ?? { label: src.id, hint: "" };
                return (
                  <li key={src.id}>
                    <label className="library-admin__source">
                      <input
                        type="checkbox"
                        checked={Boolean(src.enabled)}
                        onChange={(e) => toggleColSource(src.id, e.target.checked)}
                      />
                      <span className="library-admin__source-name">{meta.label}</span>
                      <span className="library-admin__source-hint">
                        {meta.hint}
                        {src.last && (
                          <>
                            {" "}
                            {src.last.error
                              ? t("admin.collections.source.error", { err: src.last.error })
                              : t("admin.collections.source.last", { n: src.last.added, at: new Date(src.last.at).toLocaleString() })}
                          </>
                        )}
                      </span>
                      {src.enabled && (
                        <button type="button" className="btn btn-ghost" disabled={src.running} onClick={() => syncColSource(src.id)}>
                          {src.running ? t("admin.collections.source.running") : t("admin.collections.source.sync")}
                        </button>
                      )}
                    </label>
                  </li>
                );
              })}
            </ul>
          </>
        )}
        {collections.length > 0 && (
          <ul className="library-admin__results library-admin__collections">
            {collections.map((c) => (
              <li key={c.slug}>
                <Link to={`/shelf/${encodeURIComponent(c.shelfId)}`} className="library-admin__file">
                  {c.title}
                </Link>
                <span className="library-admin__authors">
                  {" "}
                  {t("collection.count", { matched: c.matched, total: c.total })}
                  {c.source ? ` · ${c.source}` : ""}
                  {c.origin && c.origin !== "bundled" && colSources?.some((s) => s.id === c.origin && !s.enabled)
                    ? ` · ${t("admin.collections.hiddenSource")}`
                    : ""}
                </span>
                <span className="library-admin__collection-actions">
                  <button type="button" className="btn btn-ghost" disabled={colBusy} onClick={() => rematch(c.slug)}>
                    {t("admin.collections.rematch")}
                  </button>
                  <button type="button" className="btn btn-ghost" disabled={colBusy} onClick={() => removeCollection(c)}>
                    {t("admin.collections.delete")}
                  </button>
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
      )}

      {tab === "books" && openPanels.import && (
      <section className="library-admin__section library-admin__spoiler-panel">
        <h2>{t("admin.import")}</h2>
        <p className="library-admin__hint">
          {t("admin.import.hint")}
        </p>

        <div className="library-admin__import-row">
          <input
            type="file"
            accept=".inpx"
            onChange={(e) => setInpxFile(e.target.files?.[0] ?? null)}
            disabled={importRunning}
          />
          <label className="library-admin__replace">
            <input
              type="checkbox"
              checked={replace}
              onChange={(e) => setReplace(e.target.checked)}
              disabled={importRunning}
            />
            {t("admin.import.replace")}
          </label>
          <button
            type="button"
            className="btn btn-primary"
            onClick={startImport}
            disabled={(!inpxFile && !inpxPath.trim()) || importRunning}
          >
            {t("admin.import.go")}
          </button>
        </div>

        <div className="library-admin__import-path">
          <span className="library-admin__import-or">{t("admin.import.or")}</span>
          <input
            type="text"
            placeholder={t("admin.import.pathPlaceholder")}
            value={inpxPath}
            onChange={(e) => setInpxPath(e.target.value)}
            disabled={importRunning}
          />
          <p className="library-admin__hint">{t("admin.import.pathHint")}</p>
        </div>

        {!importStatusHidden &&
          status &&
          (status.running || status.phase === "done" || status.phase === "error") && (
            <div className="library-admin__genre-status library-admin__import-status">
              <div className="library-admin__genre-status-head">
                <span className="library-admin__genre-mode">{t("admin.import.statusTitle")}</span>
                <span
                  className={`library-admin__genre-progress ${
                    status.phase === "error"
                      ? "is-error"
                      : status.phase === "done" && !status.running
                        ? "is-done"
                        : ""
                  }`}
                >
                  {status.running && (
                    <>
                      <span className="library-admin__spinner" aria-hidden="true" />
                      {PHASE_LABELS()[status.phase] ?? status.phase}
                      {status.phase === "loading" && status.processed > 0 && (
                        <>{t("admin.import.processed", { n: status.processed.toLocaleString() })}</>
                      )}
                    </>
                  )}
                  {!status.running && status.phase === "done" && (
                    <>
                      {t("admin.import.done", {
                        books: Number(status.books).toLocaleString(),
                        authors: Number(status.authors).toLocaleString(),
                        series: Number(status.series).toLocaleString(),
                      })}
                    </>
                  )}
                  {!status.running && status.phase === "error" && (
                    <>{t("admin.import.error", { err: status.error })}</>
                  )}
                </span>
              </div>
              <div className="library-admin__genre-bar">
                <span
                  className={
                    status.running ? "is-running" : status.phase === "error" ? "is-error" : ""
                  }
                  style={{ width: "100%" }}
                />
              </div>
              {status.warning && (
                <div className="library-admin__status-note">⚠ {status.warning}</div>
              )}
              {!status.running && (
                <div className="library-admin__status-close">
                  <button
                    type="button"
                    className="btn btn-link"
                    onClick={() => setImportStatusHidden(true)}
                  >
                    {t("admin.progress.close")}
                  </button>
                </div>
              )}
            </div>
          )}
      </section>
      )}

      {tab === "sources" && (
      <section className="library-admin__section">
        <h2>{t("admin.sources.health")}</h2>
        <p className="library-admin__hint">{t("admin.sources.healthHint")}</p>
        <div className="library-admin__source-check-row">
          <button
            type="button"
            className="btn btn-primary"
            onClick={runSourceChecks}
            disabled={sourceCheckBusy || !enrichment}
          >
            {sourceCheckBusy ? t("admin.sources.checking") : t("admin.sources.check")}
          </button>
          {sourceCheckedAt && (
            <span className="library-admin__source-check-time">
              {t("admin.sources.checkedAt", {
                time: new Date(sourceCheckedAt).toLocaleTimeString(),
              })}
            </span>
          )}
        </div>
      </section>
      )}

      {tab === "sources" && (
      <section className="library-admin__section">
        <h2>{t("admin.sources")}</h2>
        <p className="library-admin__hint">
          {t("admin.sources.hint")}
        </p>
        {!enrichment && <div className="library-admin__hint">{t("admin.sources.loading")}</div>}
        {enrichment && (
          <ul className="library-admin__sources">
            {ENRICH_SOURCES().map((src) => (
              <li key={src.id}>
                <label className="library-admin__source">
                  <input
                    type="checkbox"
                    checked={Boolean(enrichment[src.id])}
                    onChange={() => toggleSource(src.id)}
                  />
                  <span className="library-admin__source-name">{src.label}</span>
                  <span className="library-admin__source-hint">{src.hint}</span>
                </label>
                <SourceCheckBadge check={sourceChecks?.[src.id]} />
              </li>
            ))}
          </ul>
        )}
        {enrichment && (
          <div className="library-admin__google-key">
            <span className="library-admin__source-name">{t("admin.google.key")}</span>
            <div className="library-admin__key-row">
              <input
                type="text"
                placeholder={t("admin.google.keyPlaceholder")}
                aria-label={t("admin.google.key")}
                value={googleBooksKey}
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => setGoogleBooksKey(e.target.value)}
              />
              <button type="button" className="btn btn-primary" onClick={saveGoogleBooksKey}>
                {t("admin.google.keySave")}
              </button>
            </div>
            <p className="library-admin__hint">
              {t("admin.google.keyHint")}{" "}
              <a
                href="https://developers.google.com/books/docs/v1/using#APIKey"
                target="_blank"
                rel="noreferrer"
              >
                {t("admin.google.keyDocs")}
              </a>
            </p>
          </div>
        )}
        {enrichment && (
          <div className="library-admin__genre-sync">
            <div className="library-admin__genre-actions">
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => startRatingSync("all")}
                disabled={Boolean(ratingStatus?.running) || !anyRatingSource}
              >
                {t("admin.ratings.sync")}
              </button>
              <button
                type="button"
                className="btn btn-ghost"
                onClick={() => startRatingSync("empty")}
                disabled={Boolean(ratingStatus?.running) || !anyRatingSource}
              >
                {t("admin.ratings.syncEmpty")}
              </button>
            </div>
            {!ratingStatusHidden &&
              ratingStatus &&
              (ratingStatus.running || ratingStatus.processed > 0) && (
                <div className="library-admin__genre-status">
                  <div className="library-admin__genre-status-head">
                    <span className="library-admin__genre-mode">
                      {ratingStatus.mode === "empty"
                        ? t("admin.ratings.modeEmpty")
                        : t("admin.ratings.modeAll")}
                    </span>
                    <span className="library-admin__genre-progress">
                      {t("admin.ratings.progress", {
                        processed: Number(ratingStatus.processed).toLocaleString(),
                        total: Number(ratingStatus.total).toLocaleString(),
                        updated: Number(ratingStatus.updated).toLocaleString(),
                      })}
                    </span>
                  </div>
                  <div
                    className="library-admin__genre-bar"
                    role="progressbar"
                    aria-valuemin="0"
                    aria-valuemax={Number(ratingStatus.total) || 0}
                    aria-valuenow={Number(ratingStatus.processed) || 0}
                  >
                    <span style={{ width: `${ratingPercent}%` }} />
                  </div>
                  {!ratingStatus.running && (
                    <div className="library-admin__status-close">
                      <button
                        type="button"
                        className="btn btn-link"
                        onClick={() => { setRatingStatusHidden(true); try { localStorage.setItem(RATING_STATUS_DISMISSED_KEY, "1"); } catch { /* ignore */ } }}
                      >
                        {t("admin.progress.close")}
                      </button>
                    </div>
                  )}
                </div>
              )}
            {!ratingStatusHidden && ratingStatus?.error && (
              <div className="library-admin__genre-error">{ratingStatus.error}</div>
            )}
            <p className="library-admin__hint library-admin__genre-note">
              {t("admin.ratings.background")}
            </p>
            <p className="library-admin__hint library-admin__genre-note">
              {t("admin.ratings.cacheHint")}
            </p>
          </div>
        )}
      </section>
      )}

      {tab === "sources" && (
      <section className="library-admin__section">
        <h2>{t("admin.tags")}</h2>
        <p className="library-admin__hint">{t("admin.tags.hint")}</p>
        {!enrichment && <div className="library-admin__hint">{t("admin.sources.loading")}</div>}
        {enrichment && (
          <ul className="library-admin__sources">
            {TAG_SOURCES().map((src) => (
              <li key={src.id}>
                <label className="library-admin__source">
                  <input
                    type="checkbox"
                    checked={Boolean(tagSources[src.id])}
                    onChange={() => toggleTagSource(src.id)}
                    aria-label={src.hint}
                  />
                  <span className="library-admin__source-name">{src.label}</span>
                  <span className="library-admin__source-hint">{src.hint}</span>
                </label>
                <SourceCheckBadge check={sourceChecks?.[src.id]} />
              </li>
            ))}
          </ul>
        )}
        {enrichment && (
          <div className="library-admin__genre-sync">
            <div className="library-admin__genre-actions">
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => startGenreSync("all")}
                disabled={
                  Boolean(genreStatus?.running) ||
                  (!tagSources.livelib && !tagSources.open_library)
                }
              >
                {t("admin.genres.sync")}
              </button>
              <button
                type="button"
                className="btn btn-ghost"
                onClick={() => startGenreSync("empty")}
                disabled={
                  Boolean(genreStatus?.running) ||
                  (!tagSources.livelib && !tagSources.open_library)
                }
              >
                {t("admin.genres.syncEmpty")}
              </button>
            </div>
            {!genreStatusHidden &&
              genreStatus &&
              (genreStatus.running || genreStatus.processed > 0) && (
                <div className="library-admin__genre-status">
                  <div className="library-admin__genre-status-head">
                    <span className="library-admin__genre-mode">
                      {genreStatus.mode === "empty"
                        ? t("admin.genres.modeEmpty")
                        : t("admin.genres.modeAll")}
                    </span>
                    <span className="library-admin__genre-progress">
                      {t("admin.genres.progress", {
                        processed: Number(genreStatus.processed).toLocaleString(),
                        total: Number(genreStatus.total).toLocaleString(),
                        updated: Number(genreStatus.updated).toLocaleString(),
                      })}
                    </span>
                  </div>
                  <div
                    className="library-admin__genre-bar"
                    role="progressbar"
                    aria-valuemin="0"
                    aria-valuemax={Number(genreStatus.total) || 0}
                    aria-valuenow={Number(genreStatus.processed) || 0}
                  >
                    <span style={{ width: `${genrePercent}%` }} />
                  </div>
                  {!genreStatus.running && (
                    <div className="library-admin__status-close">
                      <button
                        type="button"
                        className="btn btn-link"
                        onClick={() => { setGenreStatusHidden(true); try { localStorage.setItem(GENRE_STATUS_DISMISSED_KEY, "1"); } catch { /* ignore */ } }}
                      >
                        {t("admin.progress.close")}
                      </button>
                    </div>
                  )}
                </div>
              )}
            {!genreStatusHidden && genreStatus?.error && (
              <div className="library-admin__genre-error">{genreStatus.error}</div>
            )}
            <p className="library-admin__hint library-admin__genre-note">
              {t("admin.genres.background")}
            </p>
          </div>
        )}
      </section>
      )}

      {tab === "sources" && (
      <section className="library-admin__section">
        <h2>{t("admin.similar")}</h2>
        <p className="library-admin__hint">
          {t("admin.similar.hint")}
        </p>
        {similar && (
          <ul className="library-admin__sources">
            <li>
              <label className="library-admin__source">
                <input type="checkbox" checked={Boolean(similar.fantlab)} onChange={() => toggleSimilar("fantlab")} />
                <span className="library-admin__source-name">{t("source.fantlab")}</span>
                <span className="library-admin__source-hint">{t("admin.fantlab.hint")}</span>
              </label>
              <SourceCheckBadge check={sourceChecks?.fantlab} />
            </li>
            <li>
              <label className="library-admin__source">
                <input type="checkbox" checked={Boolean(similar.tastedive)} onChange={() => toggleSimilar("tastedive")} />
                <span className="library-admin__source-name">TasteDive</span>
                <span className="library-admin__source-hint">{t("admin.tastedive.hint")}</span>
              </label>
              <SourceCheckBadge check={sourceChecks?.tastedive} />
            </li>
          </ul>
        )}
        <div className="library-admin__key-row">
          <input
            type="text"
            placeholder={t("admin.tastedive.key")}
            value={tastediveKey}
            onChange={(e) => setTastediveKey(e.target.value)}
          />
          <button type="button" className="btn btn-primary" onClick={saveTastediveKey}>
            {t("admin.tastedive.save")}
          </button>
        </div>
      </section>
      )}

      {tab === "readers" && (
      <section className="library-admin__section">
        <h2>{t("admin.opds")}</h2>
        <p className="library-admin__hint">{t("admin.opds.hint")}</p>
        <label className="library-admin__source library-admin__opds-toggle">
          <input type="checkbox" checked={opdsEnabled} onChange={toggleOpds} />
          <span className="library-admin__source-name">{t("admin.opds.toggle")}</span>
        </label>
        {opdsEnabled ? (
          <div className="library-admin__opds-url">
            <span className="library-admin__opds-label">{t("admin.opds.url")}</span>
            <code>{opdsUrl}</code>
            <button type="button" className="btn btn-ghost" onClick={copyOpds}>
              {opdsCopied ? t("admin.opds.copied") : t("admin.opds.copy")}
            </button>
          </div>
        ) : (
          <p className="library-admin__hint">{t("admin.opds.off")}</p>
        )}
      </section>
      )}

      {tab === "readers" && (
      <section className="library-admin__section">
        <h2>{t("admin.smtp")}</h2>
        <p className="library-admin__hint">{t("admin.smtp.hint")}</p>
        {smtp && (
          <>
            <label className="library-admin__source library-admin__opds-toggle">
              <input type="checkbox" checked={Boolean(smtp.enabled)} onChange={(e) => smtpField("enabled", e.target.checked)} />
              <span className="library-admin__source-name">{t("admin.smtp.enable")}</span>
            </label>
            <div className="library-admin__smtp-grid">
              <input placeholder={t("admin.smtp.host")} value={smtp.host || ""} onChange={(e) => smtpField("host", e.target.value)} />
              <input placeholder={t("admin.smtp.port")} value={smtp.port || ""} onChange={(e) => smtpField("port", e.target.value)} style={{ maxWidth: 110 }} />
              <select value={smtp.security || "starttls"} onChange={(e) => smtpField("security", e.target.value)}>
                <option value="starttls">STARTTLS (587)</option>
                <option value="tls">SSL/TLS (465)</option>
                <option value="none">{t("admin.smtp.none")}</option>
              </select>
              <input placeholder={t("admin.smtp.user")} value={smtp.user || ""} onChange={(e) => smtpField("user", e.target.value)} />
              <input
                type="password"
                placeholder={smtp.hasPassword ? t("admin.smtp.pwdSet") : t("admin.smtp.pwd")}
                value={smtpPwd}
                onChange={(e) => setSmtpPwd(e.target.value)}
              />
              <input placeholder={t("admin.smtp.from")} value={smtp.from || ""} onChange={(e) => smtpField("from", e.target.value)} />
              <input
                type="number"
                min="1"
                step="0.1"
                aria-label={t("admin.smtp.maxMessageMiB")}
                title={t("admin.smtp.maxMessageMiB")}
                placeholder={t("admin.smtp.maxMessageMiB")}
                value={
                  smtp.maxMessageBytes
                    ? Number((Number(smtp.maxMessageBytes) / (1024 * 1024)).toFixed(2))
                    : ""
                }
                onChange={(e) => {
                  const mib = Number(e.target.value);
                  smtpField("maxMessageBytes", mib > 0 ? Math.round(mib * 1024 * 1024) : 0);
                }}
                style={{ maxWidth: 160 }}
              />
            </div>
            <div className="library-admin__key-row">
              <button type="button" className="btn btn-primary" onClick={saveSmtp}>{t("admin.smtp.save")}</button>
              <button type="button" className="btn btn-ghost" onClick={checkSmtp}>{t("admin.smtp.check")}</button>
              {smtpMsg && (
                <span className={smtpMsg.ok ? "library-admin__ok" : "library-admin__error"}>{smtpMsg.text}</span>
              )}
            </div>
          </>
        )}
      </section>
      )}

      {tab === "maintenance" && (
      <section className="library-admin__section">
        <h2>{t("admin.maintenance")}</h2>
        <p className="library-admin__hint">{t("admin.maintenance.hint")}</p>
        <div className="library-admin__genre-actions">
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => startMaintenance("scan")}
            disabled={Boolean(maintenanceStatus?.running)}
          >
            {t("admin.maintenance.scan")}
          </button>
          <button
            type="button"
            className="btn btn-ghost"
            onClick={() => startMaintenance("fix")}
            disabled={Boolean(maintenanceStatus?.running)}
          >
            {t("admin.maintenance.fix")}
          </button>
          {maintenanceStatus?.running && (
            <button type="button" className="btn btn-ghost" onClick={stopMaintenance}>
              {t("admin.maintenance.stop")}
            </button>
          )}
        </div>
        <label className="library-admin__source library-admin__maintenance-deep">
          <input
            type="checkbox"
            checked={maintenanceDeep}
            onChange={(e) => setMaintenanceDeep(e.target.checked)}
            disabled={Boolean(maintenanceStatus?.running)}
          />
          <span className="library-admin__source-name">{t("admin.maintenance.deep")}</span>
        </label>
        {maintenanceStatus && maintenanceStatus.mode && (
          <div className="library-admin__genre-status">
            <div className="library-admin__genre-status-head">
              <span className="library-admin__genre-mode">
                {maintenanceStatus.mode === "fix"
                  ? t("admin.maintenance.modeFix")
                  : t("admin.maintenance.modeScan")}
              </span>
              <span className="library-admin__genre-progress">
                {t("admin.maintenance.progress", {
                  processed: Number(maintenanceStatus.processed).toLocaleString(),
                  total: Number(maintenanceStatus.total).toLocaleString(),
                })}
              </span>
            </div>
            <div
              className="library-admin__genre-bar"
              role="progressbar"
              aria-valuemin="0"
              aria-valuemax={Number(maintenanceStatus.total) || 0}
              aria-valuenow={Number(maintenanceStatus.processed) || 0}
            >
              <span
                className={maintenanceStatus.running ? "is-running" : maintenanceStatus.stopped ? "is-error" : ""}
                style={{ width: `${maintenancePercent}%` }}
              />
            </div>
            <div className="library-admin__maintenance-counts">
              <span>{t("admin.maintenance.needsFix", { n: Number(maintenanceStatus.needsFix).toLocaleString() })}</span>
              <span>{t("admin.maintenance.fixed", { n: Number(maintenanceStatus.fixed).toLocaleString() })}</span>
              <span>{t("admin.maintenance.archived", { n: Number(maintenanceStatus.archived).toLocaleString() })}</span>
              <span>{t("admin.maintenance.unreadable", { n: Number(maintenanceStatus.unreadable).toLocaleString() })}</span>
              <span>{t("admin.maintenance.failed", { n: Number(maintenanceStatus.failed).toLocaleString() })}</span>
            </div>
            {maintenanceStatus.stopped && (
              <div className="library-admin__status-note">{t("admin.maintenance.stopped")}</div>
            )}
            {maintenanceStatus.error && (
              <div className="library-admin__genre-error">{maintenanceStatus.error}</div>
            )}
            {maintenanceStatus.examples?.length > 0 && (
              <div className="library-admin__maintenance-examples">
                <div className="library-admin__hint">{t("admin.maintenance.examples")}</div>
                <ul>
                  {maintenanceStatus.examples.map((example, i) => (
                    <li key={i}>{example}</li>
                  ))}
                </ul>
              </div>
            )}
            {!maintenanceStatus.running && (
              <div className="library-admin__status-close">
                {maintenanceStatus.mode === "fix"
                  ? maintenanceStatus.fixed > 0
                    ? t("admin.maintenance.doneFix")
                    : maintenanceStatus.failed > 0
                      ? ""
                      : t("admin.maintenance.noProblems")
                  : maintenanceStatus.needsFix > 0 ||
                      maintenanceStatus.unreadable > 0 ||
                      maintenanceStatus.failed > 0
                    ? ""
                    : t("admin.maintenance.noProblems")}
              </div>
            )}
          </div>
        )}
      </section>
      )}
    </div>
  );
};

export default AdminLibraryPage;
