import { Outlet, useParams } from "@tanstack/react-router";

export function Root() {
  return (
    <main className="min-h-dvh bg-ground text-ink">
      <Outlet />
    </main>
  );
}

export function Inbox() {
  return <h1>Inbox</h1>;
}

export function Run() {
  const { runId } = useParams({ from: "/runs/$runId" });
  return <h1>Run {runId}</h1>;
}

export function Stats() {
  return <h1>Statistics</h1>;
}
