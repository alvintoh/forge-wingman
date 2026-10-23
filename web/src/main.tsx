import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { Autonomy } from "./pages/autonomy";
import { Inbox } from "./pages/overview";
import { Peak } from "./pages/peak";
import { Providers } from "./pages/providers";
import { Root } from "./pages/root";
import { Run } from "./pages/run";
import { Stats } from "./pages/stats";
import { Projects } from "./projects";
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

const projectsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/projects",
  component: Projects,
});

const router = createRouter({
  routeTree: rootRoute.addChildren([
    inboxRoute,
    runRoute,
    statsRoute,
    providersRoute,
    peakRoute,
    autonomyRoute,
    projectsRoute,
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
