/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ['./web/templates/**/*.html', './web/static/js/app.js', './internal/server/render.go'],
  theme: {
    extend: {
      colors: {
        brand: {
          50: '#ecfdf5', 100: '#d1fae5', 200: '#a7f3d0', 300: '#6ee7b7', 400: '#34d399',
          500: '#10b981', 600: '#059669', 700: '#047857', 800: '#065f46', 900: '#064e3b', 950: '#022c22',
        },
        // Deep blue-black used for the hero, footer and dark surfaces.
        ink: {
          700: '#1c2436', 800: '#121929', 900: '#0b111f', 950: '#060a14',
        },
      },
      fontFamily: {
        sans: ['ui-sans-serif', 'system-ui', '-apple-system', 'Segoe UI', 'Roboto', 'Noto Sans Bengali', 'Helvetica Neue', 'Arial', 'sans-serif'],
      },
      boxShadow: {
        // Layered shadows read as physical depth rather than a flat blur.
        depth: '0 1px 2px rgb(15 23 42 / .04), 0 4px 8px -2px rgb(15 23 42 / .05), 0 16px 32px -12px rgb(15 23 42 / .14)',
        'depth-lg': '0 2px 4px rgb(15 23 42 / .04), 0 12px 24px -6px rgb(15 23 42 / .10), 0 32px 64px -24px rgb(15 23 42 / .30)',
        glow: '0 0 0 1px rgb(16 185 129 / .25), 0 10px 30px -8px rgb(16 185 129 / .55)',
      },
      keyframes: {
        float: { '0%, 100%': { transform: 'translate3d(0, 0, 0)' }, '50%': { transform: 'translate3d(0, -12px, 0)' } },
        aurora: {
          '0%, 100%': { transform: 'translate3d(0, 0, 0) scale(1)' },
          '33%': { transform: 'translate3d(6%, -8%, 0) scale(1.1)' },
          '66%': { transform: 'translate3d(-6%, 6%, 0) scale(.95)' },
        },
        shine: { '0%': { backgroundPosition: '200% 0' }, '100%': { backgroundPosition: '-200% 0' } },
        'pop-in': { '0%': { opacity: '0', transform: 'scale(.6) rotateX(40deg)' }, '100%': { opacity: '1', transform: 'scale(1) rotateX(0)' } },
      },
      animation: {
        float: 'float 6s ease-in-out infinite',
        aurora: 'aurora 18s ease-in-out infinite',
        shine: 'shine 6s linear infinite',
        'pop-in': 'pop-in .6s cubic-bezier(.2,.9,.3,1.3) both',
      },
    },
  },
  plugins: [],
};
