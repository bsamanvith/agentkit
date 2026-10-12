import { ThemeProvider } from "@agentkit/ui";
import { TooltipProvider } from "@agentkit/ui/components/tooltip";
import { createBrowserRouter, RouterProvider } from "react-router";
import { useApi } from "./api";
import { ApiProvider } from "./api-provider";
import { routes } from "./routes";
import { TokenPrompt } from "./token-prompt";

/** The app behind the token prompt is inert while it is open. */
function Body({ router }: { router: ReturnType<typeof createBrowserRouter> }) {
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

export function App({ router = createBrowserRouter(routes) }: { router?: ReturnType<typeof createBrowserRouter> }) {
  return (
    <ThemeProvider>
      <TooltipProvider delayDuration={300}>
        <ApiProvider>
          <Body router={router} />
        </ApiProvider>
      </TooltipProvider>
    </ThemeProvider>
  );
}
