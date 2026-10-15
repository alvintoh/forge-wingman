import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { Autonomy, Inbox, Peak, Providers, Root, Run, Stats } from "./pages";
import "./index.css";

const rootRoute = createRootRoute({ component: Root });

const inboxRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: Inbox,
});

const runRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs/$runId",
  component: Run,
});

const statsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/stats",
  component: Stats,
});

const providersRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/providers",
  component: Providers,
});

const peakRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/peak",
  component: Peak,
});

const autonomyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/autonomy",
  component: Autonomy,
});

const router = createRouter({
  routeTree: rootRoute.addChildren([
    inboxRoute,
    runRoute,
    statsRoute,
    providersRoute,
    peakRoute,
    autonomyRoute,
  ]),
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
);
