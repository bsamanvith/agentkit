import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Gate } from "./gate";
import "./index.css";

const root = document.getElementById("root");
if (!root) throw new Error("index.html has no #root");

createRoot(root).render(
  <StrictMode>
    <Gate />
  </StrictMode>,
);
