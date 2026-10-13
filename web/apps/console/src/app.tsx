import { ThemeProvider } from "@agentkit/ui";
import { useState } from "react";
import { createBrowserRouter, RouterProvider } from "react-router";
import { useApi } from "./api";
import { ApiProvider } from "./api-provider";
import { routes } from "./routes";
import { TokenPrompt } from "./token-prompt";

/** The app behind the token prompt is inert while it is open. */
function Body({ router }: { router: Router }) {
  const { prompt } = useApi();
  return (
    <>
      <div inert={prompt !== null} className="contents">
        <RouterProvider router={router} />
      </div>
      <TokenPrompt />
    </>
  );
}

type Router = ReturnType<typeof createBrowserRouter>;

export function App({ router: given }: { router?: Router }) {
  // Made once: a router per render would add a history listener each time.
  const [router] = useState(() => given ?? createBrowserRouter(routes));
  return (
    <ThemeProvider>
      <ApiProvider>
        <Body router={router} />
      </ApiProvider>
    </ThemeProvider>
  );
}
