/** @type {import('tailwindcss').Config} */
export default {
  content: [
    './index.html',
    './src/**/*.{ts,tsx}',
    './vendor/go/ui/src/**/*.{ts,tsx}',
  ],
  // The bespoke "Liquid Glass" design system ships its own reset, so disable
  // Tailwind's preflight to avoid disturbing the existing look.
  corePlugins: { preflight: false },
  theme: { extend: {} },
  plugins: [],
};
