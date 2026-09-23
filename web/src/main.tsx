import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import {
  Outlet,
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";
import "./index.css";

const rootRoute = createRootRoute({
  component: () => (
    <main className="min-h-dvh bg-ground text-ink">
      <Outlet />
    </main>
  ),
});

const inboxRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: () => <h1>Inbox</h1>,
});

const runRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs/$runId",
  component: function Run() {
    const { runId } = runRoute.useParams();
    return <h1>Run {runId}</h1>;
  },
});

const statsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/stats",
  component: () => <h1>Statistics</h1>,
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
