import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { t } from "../i18n";
import ReaderDevicesPage from "./ReaderDevicesPage";

vi.mock("../api/kosync", () => ({
  fetchKosync: vi.fn(),
  setKosyncPassword: vi.fn(),
}));

import { fetchKosync, setKosyncPassword } from "../api/kosync";

describe("ReaderDevicesPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("shows the device password setup and connected devices", async () => {
    fetchKosync.mockResolvedValue({
      enabled: true,
      login: "reader",
      lastSync: 1700000000,
      devices: [
        { device: "PocketBook", deviceId: "DEV1", lastSync: 1700000000, documents: 3 },
      ],
    });

    render(<ReaderDevicesPage />);

    expect(await screen.findByText(t("readerDevices.title"))).toBeInTheDocument();
    expect(screen.getByText("PocketBook")).toBeInTheDocument();
    expect(screen.getByText("DEV1")).toBeInTheDocument();
    expect(screen.getByText(t("readerDevices.documents", { n: 3 }))).toBeInTheDocument();
    expect(screen.getByText(t("kosync.on"))).toBeInTheDocument();
  });

  it("saves a device password and refreshes the state", async () => {
    fetchKosync.mockResolvedValue({
      enabled: false,
      login: "reader",
      lastSync: 0,
      devices: [],
    });
    setKosyncPassword.mockResolvedValue({ enabled: true, login: "reader" });

    render(<ReaderDevicesPage />);
    const input = await screen.findByPlaceholderText(t("kosync.password"));
    fireEvent.change(input, { target: { value: "device-pass-123" } });
    fireEvent.click(screen.getByRole("button", { name: t("kosync.save") }));

    await waitFor(() => expect(setKosyncPassword).toHaveBeenCalledWith("device-pass-123"));
    expect(await screen.findByText(t("kosync.saved"))).toBeInTheDocument();
  });

  it("does not pretend sync is disabled when loading fails", async () => {
    fetchKosync.mockRejectedValue(new Error("network"));
    render(<ReaderDevicesPage />);
    expect((await screen.findAllByText(t("readerDevices.loadFail"))).length).toBeGreaterThan(0);
    expect(screen.queryByText(t("kosync.off"))).not.toBeInTheDocument();
    expect(screen.queryByText(t("readerDevices.noDevices"))).not.toBeInTheDocument();
  });

  it("renders an empty device list", async () => {
    fetchKosync.mockResolvedValue({ enabled: false, login: "reader", devices: [], lastSync: 0 });
    render(<ReaderDevicesPage />);
    expect(await screen.findByText(t("readerDevices.noDevices"))).toBeInTheDocument();
  });
});
