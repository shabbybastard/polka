import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { t } from "../i18n";
import AdminLibraryPage from "./AdminLibraryPage";

vi.mock("../api/manage", () => ({
  fetchSettings: vi.fn(),
  genreSyncStatus: vi.fn(),
  importInpx: vi.fn(),
  importStatus: vi.fn(),
  saveSettings: vi.fn(),
  syncGenres: vi.fn(),
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

import { fetchSettings, genreSyncStatus, importStatus, saveSettings } from "../api/manage";
import { fetchCollections } from "../api/collections";

const settings = {
  enrichment: { livelib: true, google_books: true, open_library: true },
  similar: { fantlab: true, tastedive: false },
  tastediveKey: "",
  homeHero: true,
  tagSources: { livelib: false, open_library: false },
  opdsEnabled: true,
  smtp: { enabled: false, security: "starttls", port: 587 },
  collectionSources: [],
  library: { configured: true, exists: true, entries: 1, dir: "/books" },
};

describe("AdminLibraryPage", () => {
  it("refreshes the shared config after hiding the home hero", async () => {
    fetchSettings.mockResolvedValue(settings);
    importStatus.mockResolvedValue({ running: false });
    genreSyncStatus.mockResolvedValue({ running: false, processed: 0, total: 0, updated: 0 });
    fetchCollections.mockResolvedValue({ collections: [] });
    saveSettings.mockResolvedValue({ ...settings, homeHero: false });
    const onConfigChange = vi.fn();

    render(
      <MemoryRouter>
        <AdminLibraryPage onConfigChange={onConfigChange} />
      </MemoryRouter>
    );

    const livelib = await screen.findByLabelText(t("admin.tags.livelib"));
    expect(screen.getByRole("button", { name: t("admin.genres.syncEmpty") })).toBeInTheDocument();
    fireEvent.click(livelib);
    await waitFor(() =>
      expect(saveSettings).toHaveBeenCalledWith({
        tagSources: { livelib: true, open_library: false },
      })
    );

    fireEvent.click(await screen.findByLabelText(t("admin.home.hero")));
    await waitFor(() => expect(saveSettings).toHaveBeenCalledWith({ homeHero: false }));
    await waitFor(() => expect(onConfigChange).toHaveBeenCalledTimes(1));
  });
});
