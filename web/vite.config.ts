import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';

const nexusProxyTarget = process.env.NEXUS_DEV_PROXY_TARGET || 'http://127.0.0.1:18777';
const visualBypassAuth = process.env.NEXUS_VISUAL_BYPASS_AUTH === 'true';
const devAPIToken = process.env.NEXUS_DEV_API_TOKEN || '';

function visualDevAuth(): Plugin {
  return {
    name: 'nexus-visual-dev-auth',
    configureServer(server) {
      if (!visualBypassAuth) return;

      server.middlewares.use((req, res, next) => {
        const requestURL = new URL(req.url || '/', 'http://visual-dev.local');
        const pathname = requestURL.pathname;

        // 视觉测试不展示生产登录页；清理历史登录/return_to 套娃 URL。
        if (
          pathname === '/login' ||
          pathname === '/change-password' ||
          pathname.startsWith('/ui/login') ||
          pathname.startsWith('/ui/change-password')
        ) {
          res.statusCode = 302;
          res.setHeader('Location', '/ui/');
          res.end();
          return;
        }

        if (!pathname.startsWith('/v1/auth/')) {
          next();
          return;
        }

        const now = new Date();
        const absolute = new Date(now.getTime() + 30 * 24 * 60 * 60 * 1000);
        const session = {
          id: 'visual-dev-session',
          user_id: 'visual-dev-user',
          username: 'visualdev',
          display_name: 'Visual Dev',
          remember_me: false,
          ip_prefix: 'visual-dev',
          user_agent_summary: 'Vite Visual Test',
          created_at: now.toISOString(),
          last_seen_at: now.toISOString(),
          idle_expires_at: absolute.toISOString(),
          absolute_expires_at: absolute.toISOString(),
          must_change_password: false,
          csrf_token: 'visual-dev-csrf',
          current: true,
        };

        res.setHeader('Content-Type', 'application/json; charset=utf-8');
        res.setHeader('Cache-Control', 'no-store');

        if (pathname === '/v1/auth/status') {
          res.end(JSON.stringify({ ok: true, initialized: true, visual_dev_bypass: true }));
          return;
        }
        if (pathname === '/v1/auth/session') {
          res.end(JSON.stringify({ ok: true, session }));
          return;
        }
        if (pathname === '/v1/auth/sessions') {
          res.end(JSON.stringify({ ok: true, items: [session] }));
          return;
        }
        if (pathname === '/v1/auth/sessions/logout-others') {
          res.end(JSON.stringify({ ok: true, revoked: 0 }));
          return;
        }
        if (pathname.startsWith('/v1/auth/sessions/')) {
          res.end(JSON.stringify({ ok: true }));
          return;
        }
        if (pathname === '/v1/auth/login') {
          res.end(JSON.stringify({ ok: true, session }));
          return;
        }
        if (pathname === '/v1/auth/logout') {
          res.end(JSON.stringify({ ok: true }));
          return;
        }
        if (pathname === '/v1/auth/credential') {
          res.end(JSON.stringify({ ok: true, reauthenticate: false }));
          return;
        }

        next();
      });
    },
  };
}

export default defineConfig({
  base: '/ui/',
  plugins: [visualDevAuth(), react()],
  build: {
    outDir: '../internal/httpx/web_dist',
    emptyOutDir: true
  },
  server: {
    port: 5173,
    proxy: {
      // NexusDock 用 Origin 与 Host 做同源 CSRF 校验；开发代理必须保留浏览器访问的 Host。
      // 视觉模式只在 Vite -> 回环后端这一步注入内部 API Token，浏览器永远看不到它。
      '/v1': {
        target: nexusProxyTarget,
        changeOrigin: false,
        headers: visualBypassAuth && devAPIToken ? { Authorization: `Bearer ${devAPIToken}` } : undefined,
      },
      '/health': { target: nexusProxyTarget, changeOrigin: false }
    }
  }
});
