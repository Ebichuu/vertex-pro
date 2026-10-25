import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath } from 'node:url';
export default defineConfig(function (_a) {
    var mode = _a.mode;
    return ({
        resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
        define: mode === 'test' ? {
            'process.env.NODE_ENV': JSON.stringify('test'),
        } : {},
        plugins: [react(), tailwindcss()],
        test: {
            environment: 'jsdom',
            setupFiles: './src/test/setup.ts',
        },
    });
});
