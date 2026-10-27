import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import {
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
} from "@tanstack/react-router";
import { Autonomy } from "./pages/autonomy";
import { Configuration } from "./pages/config";
import { Inbox } from "./pages/overview";
import { Root } from "./pages/root";
import { Run } from "./pages/run";
import { Stats } from "./pages/stats";
import { validateProjectSearch } from "./project-search";
import { Projects } from "./projects";
import "./index.css";

const rootRoute = createRootRoute({ component: Root });

const inboxRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  validateSearch: validateProjectSearch,
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

const configRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/config",
  component: Configuration,
});

// The settings pages Configuration replaced send an old link on to it.
function toConfig(): never {
  throw redirect({ to: "/config", replace: true });
}

const providersRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/providers",
  beforeLoad: toConfig,
});

const peakRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/peak",
  beforeLoad: toConfig,
});

const autonomyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/autonomy",
  validateSearch: validateProjectSearch,
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
    configRoute,
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
