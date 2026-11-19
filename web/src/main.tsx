import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { setNonce } from "get-nonce";
import App from "./App";
import "./index.css";

// The server puts a per-request CSP nonce in <meta name="csp-nonce">.
// Radix inserts a <style> tag to lock page scroll under dialogs and
// sheets; with the nonce the strict CSP allows it.
const nonce = document.querySelector<HTMLMetaElement>('meta[name="csp-nonce"]')?.content;
if (nonce && !nonce.startsWith("__")) setNonce(nonce);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <BrowserRouter basename="/admin">
      <App />
    </BrowserRouter>
  </StrictMode>,
);
