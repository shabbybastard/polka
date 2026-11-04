import { t } from "../i18n";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import api from "../api/api";
import RatingStars from "../components/RatingStars";
import RateStars from "../components/RateStars";
import { fetchBookForm, fetchExternalEnrichment, fetchSimilarBooks } from "../api/fetchBooks";
import Shelf from "../components/Shelf";
import { deleteBook, updateBookEPUB, updateBookMetadata } from "../api/manage";
import { deleteProgress, fetchProgress, localProgress } from "../api/reader";
import { rateBook } from "../api/ratings";
import { toggleWishlist } from "../api/lists";
import { fetchReaderEmail, setReaderEmail, sendBook } from "../api/send";
import ListsMenu from "../components/ListsMenu";
import "./BookPage.css";

const formatBytes = (bytes) => {
  if (!bytes) return null;
  if (bytes > 1024 * 1024) return t("book.mb", { n: (bytes / 1024 / 1024).toFixed(1) });
  return t("book.kb", { n: (bytes / 1024).toFixed(1) });
};

const BookPage = ({ user, sync }) => {
  const { bookId } = useParams();
  const navigate = useNavigate();

  // Офлайн-кэш (только в режиме синхронизации десктоп-клиента)
  const [isOffline, setIsOffline] = useState(false);
  const [offlineBusy, setOfflineBusy] = useState(false);

  useEffect(() => {
    if (!sync) return undefined;
    let cancelled = false;
    import("../api/sync").then(({ fetchOfflineStatus }) =>
      fetchOfflineStatus(bookId)
        .then((res) => !cancelled && setIsOffline(Boolean(res.offline)))
        .catch(() => {})
    );
    return () => {
      cancelled = true;
    };
  }, [sync, bookId]);

  const toggleOffline = () => {
    if (offlineBusy) return;
    setOfflineBusy(true);
    import("../api/sync")
      .then(({ makeOffline, removeOffline }) =>
        (isOffline ? removeOffline(bookId) : makeOffline(bookId)).then((res) =>
          setIsOffline(Boolean(res.offline))
        )
      )
      .catch(() => alert(isOffline ? t("book.offline.failRm") : t("book.offline.failDl")))
      .finally(() => setOfflineBusy(false));
  };

  const removeBook = () => {
    if (!confirm(t("book.deleteConfirm"))) return;
    deleteBook(bookId)
      .then(() => navigate("/", { replace: true }))
      .catch(() => alert(t("book.deleteFail")));
  };

  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [coverFailed, setCoverFailed] = useState(false);
  const [enrichment, setEnrichment] = useState(null);
  const [enrichmentLoading, setEnrichmentLoading] = useState(false);
  const [similar, setSimilar] = useState(null); // {similar: [], external: []}
  const [readProgress, setReadProgress] = useState(0);
  const [forgetBusy, setForgetBusy] = useState(false);
  const [smtpReady, setSmtpReady] = useState(false);
  const [readerEmail, setReaderEmailState] = useState("");
  const [sendState, setSendState] = useState("idle");
  const [polkaRating, setPolkaRating] = useState(null); // {rating, count}
  const [userRating, setUserRating] = useState(0);
  const [ratingBusy, setRatingBusy] = useState(false);
  const [listIds, setListIds] = useState([]);
  const [wishlistId, setWishlistId] = useState(null);
  const [wishBusy, setWishBusy] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [editTitle, setEditTitle] = useState("");
  const [editAuthors, setEditAuthors] = useState([]);
  const [editLanguage, setEditLanguage] = useState("");
  const [editPublisher, setEditPublisher] = useState("");
  const [editYear, setEditYear] = useState("");
  const [editISBN, setEditISBN] = useState("");
  const [editDescription, setEditDescription] = useState("");
  const [editCover, setEditCover] = useState(null);
  const [editCoverPreview, setEditCoverPreview] = useState("");
  const [coverVersion, setCoverVersion] = useState("");
  const [editBusy, setEditBusy] = useState(false);
  const [editError, setEditError] = useState("");

  const inWishlist = wishlistId !== null && listIds.includes(wishlistId);

  const onListChange = (listId, added) => {
    setListIds((prev) => (added ? [...prev, listId] : prev.filter((id) => id !== listId)));
  };

  const flipWishlist = () => {
    if (wishBusy) return;
    setWishBusy(true);
    toggleWishlist(bookId, !inWishlist)
      .then((res) => {
        setWishlistId(res.listId);
        onListChange(res.listId, res.inWishlist);
      })
      .catch(() => {})
      .finally(() => setWishBusy(false));
  };

  const sendToReader = () => {
    if (sendState === "sending") return;
    let email = readerEmail;
    if (!email) {
      email = (window.prompt(t("book.send.askEmail")) || "").trim();
      if (!email) return;
    }
    setSendState("sending");
    const go = () =>
      sendBook(bookId)
        .then(() => {
          setSendState("sent");
          setTimeout(() => setSendState("idle"), 2500);
        })
        .catch(() => {
          setSendState("idle");
          alert(t("book.send.fail"));
        });
    if (email !== readerEmail) {
      setReaderEmail(email)
        .then(() => setReaderEmailState(email))
        .then(go)
        .catch(() => {
          setSendState("idle");
          alert(t("book.send.fail"));
        });
    } else {
      go();
    }
  };

  const forgetProgress = () => {
    if (forgetBusy || !confirm(t("book.forgetConfirm"))) return;
    setForgetBusy(true);
    deleteProgress(bookId)
      .then(() => {
        setReadProgress(0);
        localProgress.clear(bookId);
      })
      .catch(() => alert(t("book.forgetFail")))
      .finally(() => setForgetBusy(false));
  };

  const openEditor = () => {
    setEditTitle(data?.bookForm?.Title ?? "");
    const authors = (data?.authors ?? []).map((a) => ({
      last: a.LastName ?? "",
      first: a.FirstName ?? "",
      middle: a.MiddleName ?? "",
    }));
    setEditAuthors(authors.length ? authors : [{ last: "", first: "", middle: "" }]);
    setEditLanguage(data?.language ?? "");
    setEditPublisher(data?.publisher ?? "");
    setEditYear(data?.year ?? "");
    setEditISBN(data?.isbn ?? "");
    setEditDescription(
      data?.annotation ? String(data.annotation).replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim() : ""
    );
    setEditCover(null);
    setEditCoverPreview("");
    setEditError("");
    setEditOpen(true);
  };

  const chooseCover = (event) => {
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > 5 * 1024 * 1024) {
      setEditError(t("book.edit.coverTooLarge"));
      event.target.value = "";
      return;
    }
    if (editCoverPreview) URL.revokeObjectURL(editCoverPreview);
    setEditCover(file);
    setEditCoverPreview(URL.createObjectURL(file));
    setEditError("");
  };

  useEffect(
    () => () => {
      if (editCoverPreview) URL.revokeObjectURL(editCoverPreview);
    },
    [editCoverPreview]
  );

  const changeAuthor = (index, field, value) =>
    setEditAuthors((prev) => prev.map((a, i) => (i === index ? { ...a, [field]: value } : a)));

  const addAuthor = () =>
    setEditAuthors((prev) => [...prev, { last: "", first: "", middle: "" }]);

  const removeAuthor = (index) =>
    setEditAuthors((prev) => prev.filter((_, i) => i !== index));

  const saveMetadata = (event) => {
    event.preventDefault();
    const title = editTitle.trim();
    if (!title || editBusy) {
      if (!title) setEditError(t("book.edit.titleRequired"));
      return;
    }
    setEditBusy(true);
    setEditError("");
    const authors = editAuthors.filter((a) => a.last.trim() || a.first.trim() || a.middle.trim());
    const isEpub = data?.bookForm?.Ext === ".epub";
    let request;
    if (isEpub) {
      const form = new FormData();
      form.append("title", title);
      form.append("authors", JSON.stringify(authors));
      form.append("language", editLanguage);
      form.append("publisher", editPublisher);
      form.append("year", editYear);
      form.append("isbn", editISBN);
      form.append("description", editDescription);
      if (editCover) form.append("cover", editCover);
      request = updateBookEPUB(data.bookForm.BookID, form);
    } else {
      request = updateBookMetadata(data.bookForm.BookID, { title, authors });
    }
    request
      .then(() => {
        setEditOpen(false);
        setEditCover(null);
        setEditCoverPreview("");
        if (isEpub) {
          setCoverFailed(false);
          setCoverVersion(String(Date.now()));
        }
        return fetchBookForm({ selectedItemID: bookId }).then((res) => {
          setData(res ?? null);
          setPolkaRating(res?.polkaRating ?? null);
          setUserRating(Number(res?.userRating ?? 0));
          setListIds((res?.bookListIds ?? []).map(Number));
        });
      })
      .catch(() => setEditError(t("book.edit.fail")))
      .finally(() => setEditBusy(false));
  };

  const submitRating = (value) => {
    if (ratingBusy) return;
    setRatingBusy(true);
    rateBook(bookId, value)
      .then((res) => {
        setUserRating(res.userRating ?? 0);
        setPolkaRating(res.polkaRating ?? null);
      })
      .catch(() => {})
      .finally(() => setRatingBusy(false));
  };

  useEffect(() => {
    let cancelled = false;
    setReadProgress(0);
    setListIds([]);
    fetchProgress(bookId)
      .then((p) => !cancelled && p?.stored && setReadProgress(Number(p.progress ?? 0)))
      .catch(() => {});
    fetchReaderEmail()
      .then((res) => {
        if (cancelled) return;
        setSmtpReady(Boolean(res?.smtpReady));
        setReaderEmailState(res?.email ?? "");
      })
      .catch(() => {});
    import("../api/lists").then(({ fetchLists }) =>
      fetchLists()
        .then((res) => {
          if (cancelled) return;
          const wl = (res.lists ?? []).find((l) => l.builtin === "wishlist");
          setWishlistId(wl?.id ?? null);
        })
        .catch(() => {})
    );
    return () => {
      cancelled = true;
    };
  }, [bookId]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    setCoverFailed(false);
    setEnrichment(null);
    fetchBookForm({ selectedItemID: bookId })
      .then((res) => {
        if (cancelled) return;
        setData(res ?? null);
        setPolkaRating(res?.polkaRating ?? null);
        setUserRating(Number(res?.userRating ?? 0));
        setListIds((res?.bookListIds ?? []).map(Number));
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err);
      })
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [bookId]);

  useEffect(() => {
    if (!data?.bookForm?.BookID) return undefined;
    let cancelled = false;
    setEnrichmentLoading(true);

    const author = data.authors?.[0]
      ? [data.authors[0].LastName, data.authors[0].FirstName, data.authors[0].MiddleName]
          .filter(Boolean)
          .join(" ")
      : data.bookForm.AuthorsNames || "";

    fetchExternalEnrichment({
      bookId: data.bookForm.BookID,
      title: data.bookForm.Title,
      author,
      isbn: data.isbn,
    })
      .then((res) => !cancelled && setEnrichment(res ?? null))
      .catch(() => !cancelled && setEnrichment(null))
      .finally(() => !cancelled && setEnrichmentLoading(false));

    setSimilar(null);
    fetchSimilarBooks({
      bookId: data.bookForm.BookID,
      title: data.bookForm.Title,
      author,
    })
      .then((res) => !cancelled && setSimilar(res ?? null))
      .catch(() => !cancelled && setSimilar(null));

    return () => {
      cancelled = true;
    };
  }, [data]);

  const authorsLine = useMemo(() => {
    if (!data?.authors?.length) return data?.bookForm?.AuthorsNames ?? "";
    return data.authors
      .map((a) => [a.LastName, a.FirstName, a.MiddleName].filter(Boolean).join(" "))
      .join(", ");
  }, [data]);

  const seriesLine = useMemo(() => {
    if (!data?.series?.length) return null;
    return data.series
      .map((s) => (s.SeqNumber ? `${s.SeriesTitle}, № ${s.SeqNumber}` : s.SeriesTitle))
      .join("; ");
  }, [data]);

  if (loading) {
    return (
      <div className="container book-page">
        <div className="book-page__layout book-page__layout--loading">
          <div className="skeleton book-page__cover-skel" />
          <div className="book-page__info">
            <div className="skeleton book-page__line" style={{ width: "60%", height: 36 }} />
            <div className="skeleton book-page__line" style={{ width: "40%" }} />
            <div className="skeleton book-page__line" style={{ width: "100%", height: 96 }} />
          </div>
        </div>
      </div>
    );
  }

  if (error || !data?.bookForm) {
    return (
      <div className="container book-page">
        <div className="book-page__error">
          <p>{t("book.loadError")}</p>
          <Link to="/" className="btn btn-ghost">
            {t("book.toHome")}
          </Link>
        </div>
      </div>
    );
  }

  const { bookForm, annotation, publisher, city, year, isbn } = data;
  const { BookID, Title, LibRate, BookSize, Genres, Ext, FileName } = bookForm;

  const canRead = [".fb2", ".txt", ".pdf", ".epub"].includes(Ext);
  const canCompact = [".fb2", ".epub"].includes(Ext);
  const isEpub = Ext === ".epub";
  const compactLabel = Ext === ".epub" ? t("book.downloadText") : t("book.downloadCompact", { ext: Ext });

  const initials = (Title || "").trim().slice(0, 2).toUpperCase();

  const SOURCE_LABELS = {
    google_books: "Google Books",
    open_library: "Open Library",
    livelib: "LiveLib",
  };

  const primary = enrichment?.primary ?? null;
  const externalRating = primary?.rating;
  const externalRatingsCount = primary?.ratingsCount;
  const externalCoverUrl = primary?.coverUrl ?? enrichment?.coverUrl;
  const externalInfoUrl = primary?.infoUrl;
  const externalSource = primary ? (SOURCE_LABELS[primary.source] ?? primary.source) : t("book.rating.external");
  const externalIsNegative = enrichment?.negative === true;
  const extraSourceCount = enrichment?.extraSourceCount ?? 0;

  return (
    <div className="container book-page">
      <button type="button" className="book-page__back btn btn-link" onClick={() => navigate(-1)}>
        {t("back")}
      </button>

      <div className="book-page__layout">
        <aside className="book-page__cover">
          {coverFailed && externalCoverUrl ? (
            <img src={externalCoverUrl} alt={Title} referrerPolicy="no-referrer" />
          ) : coverFailed ? (
            <div className="book-page__cover-fallback">
              <span>{initials || "?"}</span>
            </div>
          ) : (
            <img
              src={api.coverUrl(BookID, coverVersion)}
              alt={Title}
              onError={() => setCoverFailed(true)}
            />
          )}
          {readProgress > 0 && (
            <div className="book-page__progress" title={`${Math.round(readProgress * 100)}%`}>
              <div
                className="book-page__progress-fill"
                style={{ width: `${Math.max(2, Math.round(readProgress * 100))}%` }}
              />
            </div>
          )}
        </aside>

        <section className="book-page__info">
          {seriesLine && <div className="book-page__series">{seriesLine}</div>}

          <h1 className="book-page__title">{Title}</h1>

          {data?.authors?.length ? (
            <div className="book-page__authors">
              {data.authors.map((a, i) => {
                const name = [a.LastName, a.FirstName, a.MiddleName].filter(Boolean).join(" ");
                return (
                  <span key={a.AuthorID || i}>
                    {i > 0 && ", "}
                    {a.AuthorID ? (
                      <Link to={`/author/${a.AuthorID}`} className="book-page__author-link">
                        {name}
                      </Link>
                    ) : (
                      name
                    )}
                  </span>
                );
              })}
            </div>
          ) : (
            authorsLine && <div className="book-page__authors">{authorsLine}</div>
          )}

          <div className="book-page__ratings">
            <div className="book-page__rating-block">
              <span className="book-page__rating-source">{t("book.rating.polka")}</span>
              {polkaRating?.count > 0 ? (
                <>
                  <RatingStars value={polkaRating.rating} size="lg" />
                  <span className="book-page__rating-meta">
                    {t("book.rating.count", { n: polkaRating.count })}
                  </span>
                </>
              ) : (
                <span className="book-page__rating-empty">{t("book.rating.none")}</span>
              )}
              <div className="book-page__rate-own">
                <span className="book-page__rate-own-label">
                  {userRating > 0 ? t("book.rate.yours") : t("book.rate.do")}
                </span>
                <RateStars value={userRating} onRate={submitRating} disabled={ratingBusy} />
              </div>
            </div>
            <div className="book-page__rating-block">
              <span className="book-page__rating-source">{t("book.rating.lib")}</span>
              {LibRate > 0 ? (
                <RatingStars value={LibRate} size="lg" />
              ) : (
                <span className="book-page__rating-empty">{t("book.rating.none")}</span>
              )}
            </div>
            <div className="book-page__rating-block book-page__rating-block--external">
              <span className="book-page__rating-source">
                {externalInfoUrl ? (
                  <a href={externalInfoUrl} target="_blank" rel="noreferrer">
                    {externalSource}
                  </a>
                ) : (
                  externalSource
                )}
              </span>
              {enrichmentLoading ? (
                <span className="book-page__rating-empty">…</span>
              ) : externalRating > 0 ? (
                <>
                  <RatingStars value={externalRating} size="lg" max={5} />
                  {externalRatingsCount > 0 && (
                    <span className="book-page__rating-meta">
                      {t("book.rating.count", { n: externalRatingsCount.toLocaleString() })}
                      {extraSourceCount > 0 && t("book.rating.more", { n: extraSourceCount })}
                    </span>
                  )}
                </>
              ) : (
                <span className="book-page__rating-empty">
                  {externalIsNegative ? t("book.rating.notFound") : t("book.rating.none")}
                </span>
              )}
            </div>
          </div>

          {data?.genresList?.length ? (
            <div className="book-page__genres">
              {data.genresList.map((g) => (
                <Link className="tag" key={g.code} to={`/shelf/genre_${g.code}`}>
                  {g.name}
                </Link>
              ))}
            </div>
          ) : (
            Genres && (
              <div className="book-page__genres">
                {Genres.split(", ").map((g) => (
                  <span className="tag" key={g}>
                    {g}
                  </span>
                ))}
              </div>
            )
          )}

          <div className="book-page__actions">
            <div className="book-page__actions-row">
              {canRead && (
                <Link className="btn btn-primary" to={`/read/${BookID}`}>
                  {readProgress > 0
                    ? t("book.continue", { p: Math.round(readProgress * 100) })
                    : t("book.read")}
                </Link>
              )}
              <a className="btn btn-ghost" href={api.fb2Url(BookID)}>
                {t("book.download", { ext: Ext || ".fb2" })}
              </a>
              <a className="btn btn-ghost" href={api.zipUrl(BookID)}>
                {t("book.downloadZip")}
              </a>
              {canCompact && (
                <a className="btn btn-ghost" href={api.fb2CompactUrl(BookID)}>
                  {compactLabel}
                </a>
              )}
            </div>

            <div className="book-page__actions-row">
              <button
                type="button"
                className={`btn btn-ghost book-page__wish ${inWishlist ? "is-on" : ""}`}
                onClick={flipWishlist}
                disabled={wishBusy}
                title={inWishlist ? t("book.wish.remove") : t("book.wish.add")}
              >
                {inWishlist ? t("book.wished") : t("book.wish")}
              </button>
              <ListsMenu bookId={Number(bookId)} memberIds={listIds} onChange={onListChange} />
              {smtpReady && (
                <button
                  type="button"
                  className="btn btn-ghost"
                  onClick={sendToReader}
                  disabled={sendState === "sending"}
                  title={t("book.send.hint")}
                >
                  {sendState === "sent"
                    ? t("book.send.done")
                    : sendState === "sending"
                      ? t("book.send.sending")
                      : t("book.send")}
                </button>
              )}
              {sync && (
                <button
                  type="button"
                  className={`btn btn-ghost book-page__wish ${isOffline ? "is-on" : ""}`}
                  onClick={toggleOffline}
                  disabled={offlineBusy}
                  title={isOffline ? t("book.offline.remove") : t("book.offline.add")}
                >
                  {offlineBusy ? "…" : isOffline ? t("book.offline.on") : t("book.offline")}
                </button>
              )}
            </div>

            {(readProgress > 0 || user?.role === "admin") && (
              <div className="book-page__actions-row book-page__actions-row--tools">
                {readProgress > 0 && (
                  <button
                    type="button"
                    className="btn btn-ghost book-page__forget"
                    onClick={forgetProgress}
                    disabled={forgetBusy}
                    title={t("book.forgetHint")}
                  >
                    {t("book.forget")}
                  </button>
                )}
                {user?.role === "admin" && (
                  <>
                    <button type="button" className="btn btn-ghost book-page__edit" onClick={openEditor}>
                      {t("book.edit")}
                    </button>
                    <button type="button" className="btn btn-ghost book-page__delete" onClick={removeBook}>
                      {t("book.delete")}
                    </button>
                  </>
                )}
              </div>
            )}
          </div>

          {annotation && (
            <article className="book-page__annotation">
              <h3>{t("book.annotation")}</h3>
              <div dangerouslySetInnerHTML={{ __html: annotation }} />
            </article>
          )}

          <dl className="book-page__facts">
            {publisher && (
              <>
                <dt>{t("book.publisher")}</dt>
                <dd>{publisher}</dd>
              </>
            )}
            {(city || year) && (
              <>
                <dt>{t("book.published")}</dt>
                <dd>{[city, year].filter(Boolean).join(", ")}</dd>
              </>
            )}
            {isbn && (
              <>
                <dt>ISBN</dt>
                <dd>{isbn}</dd>
              </>
            )}
            {BookSize > 0 && (
              <>
                <dt>{t("book.size")}</dt>
                <dd>{formatBytes(BookSize)}</dd>
              </>
            )}
            {FileName && (
              <>
                <dt>{t("book.file")}</dt>
                <dd className="book-page__filename">
                  {FileName}
                  {Ext}
                </dd>
              </>
            )}
          </dl>
        </section>
      </div>

      {(similar?.similar?.length > 0 || similar?.external?.length > 0) && (
        <section className="book-page__similar">
          {similar.similar?.length > 0 && (
            <Shelf
              title={t("book.similar")}
              subtitle={t("book.similar.sub")}
              books={similar.similar}
            />
          )}
          {similar.external?.length > 0 && (
            <div className="book-page__similar-external">
              <h3>{t("book.similar.external")}</h3>
              <ul>
                {similar.external.map((e, i) => (
                  <li key={`${e.title}-${i}`}>
                    <span className="book-page__similar-title">{e.title}</span>
                    {e.author && <span className="book-page__similar-author"> — {e.author}</span>}
                    <span className="tag book-page__similar-src">
                      {e.source === "fantlab" ? t("source.fantlab") : e.source === "tastedive" ? t("source.tastedive") : e.source}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </section>
      )}

      {editOpen && (
        <div className="book-page__modal" role="dialog" aria-modal="true" aria-label={t("book.edit")}>
          <form className="book-page__modal-card" onSubmit={saveMetadata}>
            <h2>{t("book.edit")}</h2>
            <label className="book-page__modal-field">
              <span>{t("book.edit.title")}</span>
              <input
                type="text"
                value={editTitle}
                onChange={(e) => setEditTitle(e.target.value)}
                maxLength={512}
                autoFocus
              />
            </label>
            {isEpub && (
              <>
                <div className="book-page__modal-grid">
                  <label className="book-page__modal-field">
                    <span>{t("book.edit.language")}</span>
                    <input
                      type="text"
                      value={editLanguage}
                      onChange={(e) => setEditLanguage(e.target.value)}
                      maxLength={32}
                    />
                  </label>
                  <label className="book-page__modal-field">
                    <span>{t("book.edit.year")}</span>
                    <input
                      type="text"
                      value={editYear}
                      onChange={(e) => setEditYear(e.target.value)}
                      maxLength={32}
                    />
                  </label>
                </div>
                <div className="book-page__modal-grid">
                  <label className="book-page__modal-field">
                    <span>{t("book.edit.publisher")}</span>
                    <input
                      type="text"
                      value={editPublisher}
                      onChange={(e) => setEditPublisher(e.target.value)}
                      maxLength={256}
                    />
                  </label>
                  <label className="book-page__modal-field">
                    <span>{t("book.edit.isbn")}</span>
                    <input
                      type="text"
                      value={editISBN}
                      onChange={(e) => setEditISBN(e.target.value)}
                      maxLength={32}
                    />
                  </label>
                </div>
                <label className="book-page__modal-field">
                  <span>{t("book.edit.description")}</span>
                  <textarea
                    value={editDescription}
                    onChange={(e) => setEditDescription(e.target.value)}
                    rows={4}
                    maxLength={4096}
                  />
                </label>
                <div className="book-page__cover-edit">
                  <div>
                    <span className="book-page__modal-label">{t("book.edit.cover")}</span>
                    <input type="file" accept="image/jpeg,image/png,image/webp" onChange={chooseCover} />
                    <p className="book-page__modal-hint">{t("book.edit.coverHint")}</p>
                  </div>
                  <img
                    className="book-page__cover-preview"
                    src={editCoverPreview || api.coverUrl(BookID, coverVersion)}
                    alt=""
                  />
                </div>
              </>
            )}
            <div className="book-page__modal-authors">
              <span>{t("book.edit.authors")}</span>
              {editAuthors.map((a, i) => (
                <div className="book-page__author-row" key={i}>
                  <input
                    type="text"
                    placeholder={t("book.edit.last")}
                    value={a.last}
                    onChange={(e) => changeAuthor(i, "last", e.target.value)}
                    maxLength={128}
                  />
                  <input
                    type="text"
                    placeholder={t("book.edit.first")}
                    value={a.first}
                    onChange={(e) => changeAuthor(i, "first", e.target.value)}
                    maxLength={128}
                  />
                  <input
                    type="text"
                    placeholder={t("book.edit.middle")}
                    value={a.middle}
                    onChange={(e) => changeAuthor(i, "middle", e.target.value)}
                    maxLength={128}
                  />
                  <button
                    type="button"
                    className="btn btn-link book-page__author-remove"
                    onClick={() => removeAuthor(i)}
                    aria-label={t("book.edit.removeAuthor")}
                  >
                    ×
                  </button>
                </div>
              ))}
              <button type="button" className="btn btn-link" onClick={addAuthor}>
                + {t("book.edit.addAuthor")}
              </button>
            </div>
            {editError && <div className="book-page__error">{editError}</div>}
            <div className="book-page__modal-actions">
              <button
                type="button"
                className="btn btn-ghost"
                onClick={() => setEditOpen(false)}
                disabled={editBusy}
              >
                {t("book.edit.cancel")}
              </button>
              <button type="submit" className="btn btn-primary" disabled={editBusy}>
                {t("book.edit.save")}
              </button>
            </div>
          </form>
        </div>
      )}
    </div>
  );
};

export default BookPage;
