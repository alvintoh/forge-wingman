import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { Inbox, Root, Run, Stats } from "./pages";
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

const router = createRouter({
  routeTree: rootRoute.addChildren([inboxRoute, runRoute, statsRoute]),
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
