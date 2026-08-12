import { NavLink, Outlet, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth/AuthContext';
import { Badge, TimeCell } from './ui';
import { cn, shortId } from '../lib/format';

interface NavItem {
  to: string;
  label: string;
  icon: string;
  end?: boolean;
}

const PRIMARY_NAV: NavItem[] = [
  { to: '/', label: '概览', icon: '◧', end: true },
  { to: '/servers', label: '服务器', icon: '▤' },
  { to: '/clusters', label: '集群', icon: '◈' },
  { to: '/operations', label: '运维操作', icon: '▶' },
  { to: '/release-cache', label: 'Release 缓存', icon: '⬡' },
  { to: '/primary-transfers', label: '主节点迁移', icon: '⇆' },
  { to: '/commands', label: '命令中心', icon: '⚙' },
  { to: '/tasks', label: '任务', icon: '☰' },
  { to: '/ai-credentials', label: 'AI 凭证', icon: '🔑' },
  { to: '/api-tokens', label: 'API Token', icon: '🎫' },
  { to: '/api-directory', label: '接口中心', icon: '📖' },
  { to: '/audit', label: '审计日志', icon: '📋' },
  { to: '/settings', label: '系统设置', icon: '⚙' },
];

const CHILD_NAV: NavItem[] = [
  { to: '/', label: '本机概览', icon: '◧', end: true },
  { to: '/commands', label: '本机命令', icon: '⚙' },
  { to: '/tasks', label: '本机任务', icon: '☰' },
  { to: '/ai-credentials', label: '本机 AI 凭证', icon: '🔑' },
  { to: '/audit', label: '本机审计', icon: '📋' },
  { to: '/settings', label: '受限本机设置', icon: '⚙' },
];

export function Layout() {
  const { session, logout } = useAuth();
  const navigate = useNavigate();

  if (!session) return null;
  const isProd = session.environment === 'production';
  const nav = session.role === 'primary' ? PRIMARY_NAV : CHILD_NAV;

  const handleLogout = async () => {
    await logout();
    navigate('/login', { replace: true });
  };

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="topbar-left">
          <div className="brand">
            <span className="brand-mark">SC</span>
            <span className="brand-name">ServerCLI</span>
          </div>
          <Badge tone={isProd ? 'red' : 'blue'}>{isProd ? '正式环境' : '测试环境'}</Badge>
          <Badge tone={session.role === 'primary' ? 'indigo' : 'teal'}>
            {session.role === 'primary' ? '主服务器' : '子服务器'}
          </Badge>
        </div>
        <div className="topbar-right">
          <span className="topbar-item" title={session.nodeId ? `node_id: ${session.nodeId}` : undefined}>
            <span className="muted">服务器</span> {session.nodeName || '—'}
            {session.nodeIp && <span className="muted"> ({session.nodeIp})</span>}
            {session.nodeId && <span className="mono muted"> #{shortId(session.nodeId)}</span>}
          </span>
          <span className="topbar-item">
            <span className="muted">管理员</span> {session.adminUsername || 'admin'}
          </span>
          {session.expiresAt && (
            <span className="topbar-item" title="会话过期时间（UTC）">
              <span className="muted">会话至</span> <TimeCell value={session.expiresAt} />
            </span>
          )}
          <button className="btn btn-ghost btn-sm" onClick={handleLogout}>
            退出
          </button>
        </div>
      </header>
      <div className="app-body">
        <nav className="sidebar" aria-label="主导航">
          <ul>
            {nav.map((item) => (
              <li key={item.to}>
                <NavLink
                  to={item.to}
                  end={item.end}
                  className={({ isActive }) => cn('nav-item', isActive && 'nav-active')}
                >
                  <span className="nav-icon" aria-hidden>
                    {item.icon}
                  </span>
                  {item.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
        <main className="content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
