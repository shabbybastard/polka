import "@fontsource-variable/inter";
import { createRoot } from "react-dom/client";
import Root from "./Root";
import { initTheme } from "./theme";

initTheme();

createRoot(document.getElementById("root")).render(<Root />);
