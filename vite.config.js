import { sveltekit } from "@sveltejs/kit/vite";
import { nodePolyfills } from "vite-plugin-node-polyfills";

/** @type {import('vite').UserConfig} */
const config = {
  build: {
    target: 'es2021'
  },
  plugins: [
    sveltekit(),
    nodePolyfills({
      globals: {
        Buffer: true
      }
    })
  ],
  server: {
    proxy: {
      "/api": {
        // PAISA_API=https://paisa.example.com npm run dev: try the UI against a running Paisa
        target: process.env.PAISA_API || "http://localhost:7500",
        changeOrigin: true
      }
    },
    fs: {
      allow: ["./fonts"]
    }
  }
};

export default config;
