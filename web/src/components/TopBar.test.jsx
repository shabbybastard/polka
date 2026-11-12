import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { t } from "../i18n";
import TopBar from "./TopBar";

const admin = { login: "desktop", displayName: "Владелец", role: "admin" };

describe("TopBar", () => {
  it("shows the connected login and hides admin in sync mode", () => {
    render(
      <MemoryRouter>
        <TopBar
          user={admin}
          desktop
          sync={{ enabled: true, online: true, server: "https://example.org", login: "remote-user" }}
        />
      </MemoryRouter>
    );

    expect(screen.getByText("remote-user")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: t("nav.manage") })).not.toBeInTheDocument();
  });

  it("keeps the admin link in standalone desktop mode", () => {
    render(
      <MemoryRouter>
        <TopBar user={admin} desktop />
      </MemoryRouter>
    );

    expect(screen.getByRole("link", { name: t("nav.manage") })).toBeInTheDocument();
    expect(screen.getByText(t("owner"))).toBeInTheDocument();
  });
});
