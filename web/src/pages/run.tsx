import { useParams } from "@tanstack/react-router";

export function Run() {
  const { runId } = useParams({ from: "/runs/$runId" });
  return <h1>Run {runId}</h1>;
}
