import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { readFileSync } from "node:fs";

const tlsKeyFile = process.env.ADMIN_DEV_TLS_KEY_FILE;
const tlsCertFile = process.env.ADMIN_DEV_TLS_CERT_FILE;
if (Boolean(tlsKeyFile) !== Boolean(tlsCertFile)) {
  throw new Error("Both administrator development TLS files are required.");
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    https: tlsKeyFile && tlsCertFile
      ? {key: readFileSync(tlsKeyFile), cert: readFileSync(tlsCertFile)}
      : undefined,
    port: 4173,
    strictPort: true,
    proxy: {
      "/admin/api": {
        target: process.env.ADMIN_API_ORIGIN ?? "http://127.0.0.1:4174",
        changeOrigin: false,
      },
    },
  },
  build: {
    sourcemap: true,
    target: "es2022",
  },
});
