import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  clearUser: vi.fn(),
  push: vi.fn(),
  removeItem: vi.fn(),
}));
vi.mock("@/stores/auth", () => ({
  useAuthStore: () => ({ clearUser: mocks.clearUser }),
}));
vi.mock("@/router", () => ({ default: { push: mocks.push } }));
vi.mock("../constants", () => ({
  baseURL: "/browser",
  noAuth: false,
  logoutPage: "/login",
  authMethod: "json",
}));
vi.mock("@/api/utils", () => ({ StatusError: Error, setSafeTimeout: vi.fn() }));

import { logout } from "../auth";

describe("logout", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubGlobal("document", { cookie: "" });
    vi.stubGlobal("localStorage", { removeItem: mocks.removeItem });
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it.each(["network", "HTTP"])(
    "clears local state after a %s failure",
    async (failure) => {
      const fetch = vi.fn();
      if (failure === "network") fetch.mockRejectedValue(new Error("offline"));
      else fetch.mockResolvedValue({ ok: false, status: 503 });
      vi.stubGlobal("fetch", fetch);
      await expect(logout("inactivity")).resolves.toBeUndefined();
      expect(mocks.clearUser).toHaveBeenCalledOnce();
      expect(mocks.removeItem).toHaveBeenCalledWith("jwt");
      expect(mocks.push).toHaveBeenCalledWith({
        path: "/login",
        query: { "logout-reason": "inactivity" },
      });
    }
  );

  it("sends the CSRF-protecting header and redirects on success", async () => {
    const fetch = vi.fn().mockResolvedValue({ ok: true });
    vi.stubGlobal("fetch", fetch);
    await logout();
    expect(fetch).toHaveBeenCalledWith("/browser/api/logout", {
      method: "POST",
      headers: { "X-Requested-With": "FileBrowser" },
    });
    expect(mocks.clearUser).toHaveBeenCalledOnce();
    expect(mocks.push).toHaveBeenCalledWith({ path: "/login" });
  });
});
