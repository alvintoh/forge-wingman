import { Outlet } from "@tanstack/react-router";
import { InboxProvider } from "../inbox";
import { AppShell } from "../menu";

export function Root() {
  return (
    <div className="bg-ground text-ink">
      <InboxProvider>
        <AppShell>
          <Outlet />
        </AppShell>
      </InboxProvider>
    </div>
  );
}
