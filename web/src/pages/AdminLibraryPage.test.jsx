import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { t } from "../i18n";
import AdminLibraryPage from "./AdminLibraryPage";

vi.mock("../api/manage", () => ({
  checkSources: vi.fn(),
  epubMaintenanceStatus: vi.fn(),
  fetchConversions: vi.fn(),
  fetchSettings: vi.fn(),
  genreSyncStatus: vi.fn(),
  importInpx: vi.fn(),
  importStatus: vi.fn(),
  ratingSyncStatus: vi.fn(),
  saveSettings: vi.fn(),
  startEPUBMaintenance: vi.fn(),
  stopEPUBMaintenance: vi.fn(),
  syncGenres: vi.fn(),
  syncRatings: vi.fn(),
  testSmtp: vi.fn(),
  uploadBooks: vi.fn(),
}));

vi.mock("../api/collections", () => ({
  deleteCollection: vi.fn(),
  fetchCollections: vi.fn(),
  importCollection: vi.fn(),
  rematchCollection: vi.fn(),
  syncCollectionSource: vi.fn(),
}));

vi.mock("../api/auth", () => ({
  fetchUsers: vi.fn(),
  createUser: vi.fn(),
  updateUser: vi.fn(),
  deleteUser: vi.fn(),
}));

import {
  checkSources,
  epubMaintenanceStatus,
  fetchConversions,
  fetchSettings,
  genreSyncStatus,
  importStatus,
  ratingSyncStatus,
  saveSettings,
  startEPUBMaintenance,
  stopEPUBMaintenance,
} from "../api/manage";
import { fetchCollections } from "../api/collections";
import { fetchUsers } from "../api/auth";

const settings = {
  enrichment: { livelib: true, google_books: true, open_library: true },
  similar: { fantlab: true, tastedive: false },
  tastediveKey: "",
  homeHero: true,
  tagSources: { livelib: false, open_library: false },
  opdsEnabled: true,
  smtp: { enabled: false, security: "starttls", port: 587 },
  convert: { fb2ToEpub: false, keepSource: true },
  collectionSources: [],
  library: { configured: true, exists: true, entries: 1, dir: "/books" },
};

const mockBase = () => {
  fetchSettings.mockResolvedValue(settings);
  importStatus.mockResolvedValue({ running: false });
  genreSyncStatus.mockResolvedValue({ running: false, processed: 0, total: 0, updated: 0 });
  ratingSyncStatus.mockResolvedValue({ running: false, processed: 0, total: 0, updated: 0 });
  epubMaintenanceStatus.mockResolvedValue({ running: false, processed: 0, total: 0 });
  fetchCollections.mockResolvedValue({ collections: [] });
};

describe("AdminLibraryPage", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
  });

  it("keeps a dismissed completed conversion queue hidden after reload", async () => {
    mockBase();
    const job = { id: "job-1", name: "book.fb2", status: "done", updatedAt: "2026-01-01 10:00:00" };
    fetchConversions.mockResolvedValue({ jobs: [job] });

    const view = render(
      <MemoryRouter>
        <AdminLibraryPage />
      </MemoryRouter>
    );
    expect(await screen.findByText(t("admin.convert.jobs"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("admin.progress.close") }));
    await waitFor(() => expect(screen.queryByText(t("admin.convert.jobs"))).not.toBeInTheDocument());
    view.unmount();

    // Same completed job after a reload: the card stays dismissed.
    render(
      <MemoryRouter>
        <AdminLibraryPage />
      </MemoryRouter>
    );
    await waitFor(() => expect(fetchConversions).toHaveBeenCalled());
    expect(screen.queryByText(t("admin.convert.jobs"))).not.toBeInTheDocument();

    // A new conversion job is a new signature and brings the card back.
    fetchConversions.mockResolvedValue({
      jobs: [{ ...job, id: "job-2", updatedAt: "2026-01-01 11:00:00" }],
    });
    render(
      <MemoryRouter>
        <AdminLibraryPage />
      </MemoryRouter>
    );
    expect(await screen.findByText(t("admin.convert.jobs"))).toBeInTheDocument();
  });

  it("moves import and collections into collapsible blocks under Books", async () => {
    mockBase();

    render(
      <MemoryRouter>
        <AdminLibraryPage />
      </MemoryRouter>
    );

    await waitFor(() => expect(fetchSettings).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: t("admin.tab.import") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("admin.tab.collections") })).not.toBeInTheDocument();

    const importToggle = screen.getByRole("button", { name: t("admin.import") });
    const collectionsToggle = screen.getByRole("button", { name: t("admin.collections") });
    expect(importToggle).toHaveAttribute("aria-expanded", "false");
    expect(collectionsToggle).toHaveAttribute("aria-expanded", "false");

    fireEvent.click(importToggle);
    expect(importToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(t("admin.import.hint"))).toBeInTheDocument();
    expect(collectionsToggle).toHaveAttribute("aria-expanded", "false");

    fireEvent.click(importToggle);
    expect(importToggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(t("admin.import.hint"))).not.toBeInTheDocument();

    fireEvent.click(collectionsToggle);
    expect(collectionsToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("heading", { name: t("admin.collections") })).toBeInTheDocument();
  });

  it("opens the import block from a legacy tab link", async () => {
    mockBase();

    render(
      <MemoryRouter initialEntries={["/admin/library?tab=import"]}>
        <AdminLibraryPage />
      </MemoryRouter>
    );

    const importToggle = await screen.findByRole("button", { name: t("admin.import") });
    expect(importToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(t("admin.import.hint"))).toBeInTheDocument();
  });

  it("opens the collections block from a legacy tab link", async () => {
    mockBase();

    render(
      <MemoryRouter initialEntries={["/admin/library?tab=collections"]}>
        <AdminLibraryPage />
      </MemoryRouter>
    );

    const collectionsToggle = await screen.findByRole("button", { name: t("admin.collections") });
    expect(collectionsToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("heading", { name: t("admin.collections") })).toBeInTheDocument();
  });

  it("renders the users sub-tab", async () => {
    mockBase();
    fetchUsers.mockResolvedValue({ users: [] });

    render(
      <MemoryRouter>
        <AdminLibraryPage me={{ id: 1, role: "admin", login: "admin", displayName: "Admin" }} />
      </MemoryRouter>
    );

    fireEvent.click(await screen.findByRole("button", { name: t("admin.tab.users") }));
    await waitFor(() => expect(fetchUsers).toHaveBeenCalled());
    expect(await screen.findByRole("heading", { name: t("users.title") })).toBeInTheDocument();
  });

  it("refreshes the shared config after hiding the home hero", async () => {
    fetchSettings.mockResolvedValue(settings);
    fetchConversions.mockResolvedValue({ jobs: [] });
    importStatus.mockResolvedValue({ running: false });
    genreSyncStatus.mockResolvedValue({ running: false, processed: 0, total: 0, updated: 0 });
    ratingSyncStatus.mockResolvedValue({ running: false, processed: 0, total: 0, updated: 0 });
    fetchCollections.mockResolvedValue({ collections: [] });
    saveSettings.mockResolvedValue({ ...settings, homeHero: false });
    const onConfigChange = vi.fn();

    render(
      <MemoryRouter>
        <AdminLibraryPage onConfigChange={onConfigChange} />
      </MemoryRouter>
    );

    // External sources live on their own tab now.
    fireEvent.click(await screen.findByRole("button", { name: t("admin.tab.sources") }));
    const livelib = await screen.findByLabelText(t("admin.tags.livelib"));
    const genresSection = screen.getByRole("heading", { name: t("admin.tags") }).closest("section");
    expect(
      within(genresSection).getByRole("button", { name: t("admin.genres.syncEmpty") })
    ).toBeInTheDocument();
    fireEvent.click(livelib);
    await waitFor(() =>
      expect(saveSettings).toHaveBeenCalledWith({
        tagSources: { livelib: true, open_library: false },
      })
    );

    // Home hero toggle moved to the appearance tab.
    fireEvent.click(await screen.findByRole("button", { name: t("admin.tab.appearance") }));
    fireEvent.click(await screen.findByLabelText(t("admin.home.hero")));
    await waitFor(() => expect(saveSettings).toHaveBeenCalledWith({ homeHero: false }));
    await waitFor(() => expect(onConfigChange).toHaveBeenCalledTimes(1));
  });

  it("checks external sources and renders their states", async () => {
    mockBase();
    checkSources.mockResolvedValue({
      checkedAt: "2026-11-09T19:30:00Z",
      sources: [
        { source: "livelib", state: "ok", status: 200, latencyMs: 412 },
        { source: "fantlab", state: "error", status: 500, message: "HTTP 500" },
        { source: "google_books", state: "missing_key" },
        { source: "open_library", state: "ok", status: 200, latencyMs: 300 },
        { source: "tastedive", state: "disabled" },
      ],
    });

    render(
      <MemoryRouter>
        <AdminLibraryPage />
      </MemoryRouter>
    );

    fireEvent.click(await screen.findByRole("button", { name: t("admin.tab.sources") }));
    fireEvent.click(await screen.findByRole("button", { name: t("admin.sources.check") }));
    await waitFor(() => expect(checkSources).toHaveBeenCalled());
    expect((await screen.findAllByText(new RegExp(t("admin.sources.state.error")))).length).toBeGreaterThan(0);
    expect((await screen.findAllByText(new RegExp(t("admin.sources.state.missingKey")))).length).toBeGreaterThan(0);
  });

  it("starts an EPUB maintenance scan", async () => {
    mockBase();
    startEPUBMaintenance.mockResolvedValue({ started: true });

    render(
      <MemoryRouter>
        <AdminLibraryPage />
      </MemoryRouter>
    );

    fireEvent.click(await screen.findByRole("button", { name: t("admin.tab.maintenance") }));
    fireEvent.click(await screen.findByRole("button", { name: t("admin.maintenance.scan") }));
    await waitFor(() => expect(startEPUBMaintenance).toHaveBeenCalledWith("scan", { deep: true }));
  });
});
