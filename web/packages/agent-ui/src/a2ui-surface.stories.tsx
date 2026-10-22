import type { Component, Surface } from "./a2ui/model";
import { A2UISurface, type SurfaceProps } from "./a2ui/surface";
import type { Meta, Story } from "./stories";

export default { title: "agent-ui/A2UISurface", component: A2UISurface } satisfies Meta<SurfaceProps>;

// The mockup's "Narrow the news search" surface (docs/UI.md).
const news: Surface = {
  id: "news",
  key: "a:ui0",
  revision: 3,
  deleted: false,
  data: { topic: "go 1.27", sources: ["news"], recent: false },
  components: new Map(
    (
      [
        { id: "root", component: "Card", child: "col" },
        { id: "col", component: "Column", children: ["title", "topic", "sources", "recent", "search"] },
        { id: "title", component: "Text", variant: "h3", text: "Narrow the news search" },
        { id: "topic", component: "TextField", label: "Topic", value: { path: "/topic" } },
        {
          id: "sources",
          component: "ChoicePicker",
          label: "Sources",
          variant: "multipleSelection",
          displayStyle: "chips",
          options: [
            { label: "News sites", value: "news" },
            { label: "Blogs", value: "blogs" },
            { label: "Forums", value: "forums" },
          ],
          value: { path: "/sources" },
        },
        { id: "recent", component: "CheckBox", label: "Past week only", value: { path: "/recent" } },
        {
          id: "search",
          component: "Button",
          variant: "primary",
          child: "search_label",
          action: { event: { name: "search", context: { topic: { path: "/topic" } } } },
        },
        { id: "search_label", component: "Text", text: "Search" },
      ] as Component[]
    ).map((c) => [c.id, c]),
  ),
};

export const Live: Story<SurfaceProps> = { args: { surface: news, onAction: () => {} } };
export const Busy: Story<SurfaceProps> = { args: { surface: news, state: "busy", onAction: () => {} } };
export const Sent: Story<SurfaceProps> = { args: { surface: news, state: "sent", onAction: () => {} } };
export const Unsupported: Story<SurfaceProps> = {
  args: {
    surface: {
      ...news,
      components: new Map([
        ["root", { id: "root", component: "Column", children: ["x"] }],
        ["x", { id: "x", component: "Slider", value: 3, max: 9 }],
      ]),
    },
  },
};
